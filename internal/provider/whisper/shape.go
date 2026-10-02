package whisper

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Cue shaping limits. They are constants rather than configuration: they
// encode what a subtitle must look like to be readable on a TV.
const (
	maxLineChars      = 42
	maxLines          = 2
	maxCharsPerSecond = 17.0
	minCueSeconds     = 1.0
	maxCueSeconds     = 7.0

	// pauseSplitSeconds is the silence between two words that always starts
	// a new cue.
	pauseSplitSeconds = 1.0

	// maxWordSeconds bounds a single word's duration. Whisper stretches the
	// last word before a long silence across that silence, which would
	// otherwise make the following cue look like a long pause.
	maxWordSeconds = 3.0
)

// word is one whole word with its timing in seconds.
type word struct {
	Text  string
	Start float64
	End   float64
}

// cue is one subtitle cue: at most maxLines lines.
type cue struct {
	Start float64
	End   float64
	Lines []string
}

func (c cue) text() string {
	return strings.Join(c.Lines, " ")
}

// shapeCues turns whisper segments into well-formed subtitle cues.
func shapeCues(segments []segment) []cue {
	words := mergeTokens(segments)
	var cues []cue
	for _, group := range groupWords(words) {
		// groupWords only emits groups that fit, so wrapping cannot fail.
		lines, _ := layoutLines(group)
		cues = append(cues, cue{
			Start: group[0].Start,
			End:   group[len(group)-1].End,
			Lines: lines,
		})
	}
	return retime(collapseRepeats(cues))
}

// mergeTokens flattens segments into whole words. A token with a leading
// space starts a new word; one without continues the previous word, even
// across a segment boundary. A segment without token timings contributes
// its text's words spread over the segment's span in proportion to length.
func mergeTokens(segments []segment) []word {
	var words []word
	startNew := true
	add := func(text string, start, end float64) {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || isSpecialToken(trimmed) {
			return
		}
		continues := len(text) == len(strings.TrimLeftFunc(text, unicode.IsSpace))
		if startNew || !continues || len(words) == 0 {
			words = append(words, word{Text: trimmed, Start: start, End: end})
		} else {
			last := &words[len(words)-1]
			last.Text += trimmed
			last.End = end
		}
		startNew = false
	}

	for _, seg := range segments {
		if len(seg.Words) == 0 {
			words = append(words, spreadWords(seg)...)
			startNew = true
			continue
		}
		for _, t := range seg.Words {
			add(t.Text, t.Start, t.End)
		}
	}

	for i := range words {
		if words[i].End-words[i].Start > maxWordSeconds {
			words[i].End = words[i].Start + maxWordSeconds
		}
	}
	return words
}

// isSpecialToken reports whether text is a whisper control token such as
// "[_BEG_]" or "[_TT_150]", which carries timing but no speech.
func isSpecialToken(text string) bool {
	return strings.HasPrefix(text, "[_") && strings.HasSuffix(text, "_]")
}

func spreadWords(seg segment) []word {
	fields := strings.Fields(seg.Text)
	total := 0
	for _, f := range fields {
		total += utf8.RuneCountInString(f)
	}
	if total == 0 {
		return nil
	}
	words := make([]word, 0, len(fields))
	at := seg.Start
	span := seg.End - seg.Start
	for _, f := range fields {
		d := span * float64(utf8.RuneCountInString(f)) / float64(total)
		words = append(words, word{Text: f, Start: at, End: at + d})
		at += d
	}
	return words
}

// groupWords partitions words into cue-sized groups, splitting at long
// pauses and sentence ends, and otherwise only when a cue is full, in which
// case it prefers to break after the last clause punctuation.
func groupWords(words []word) [][]word {
	var groups [][]word
	var cur []word

	for _, w := range words {
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			switch {
			case w.Start-prev.End >= pauseSplitSeconds:
				groups, cur = append(groups, cur), nil
			case endsSentence(prev.Text) && prev.End-cur[0].Start >= minCueSeconds:
				groups, cur = append(groups, cur), nil
			case !fits(append(cur[:len(cur):len(cur)], w)):
				head, tail := splitAtClause(cur)
				groups = append(groups, head)
				cur = tail
				if len(cur) > 0 && !fits(append(cur[:len(cur):len(cur)], w)) {
					groups, cur = append(groups, cur), nil
				}
			}
		}
		cur = append(cur, w)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// fits reports whether words can be shown as one cue: within the duration
// bound and wrappable into maxLines lines of at most maxLineChars. A single
// word always fits, however long, since it cannot be split.
func fits(words []word) bool {
	if len(words) == 1 {
		return true
	}
	if words[len(words)-1].End-words[0].Start > maxCueSeconds {
		return false
	}
	_, ok := layoutLines(words)
	return ok
}

// splitAtClause splits a full cue after its last clause punctuation, so the
// break falls where a human editor would put it. Without a usable clause
// break it returns the whole cue as head.
func splitAtClause(words []word) (head, tail []word) {
	total := utf8.RuneCountInString(joinWords(words))
	chars := 0
	best := -1
	for i, w := range words[:len(words)-1] {
		chars += utf8.RuneCountInString(w.Text) + 1
		// A break too early leaves a runt cue behind, so require the head to
		// carry a third of the text.
		if endsClause(w.Text) && chars*3 >= total {
			best = i
		}
	}
	if best < 0 {
		return words, nil
	}
	return words[:best+1], words[best+1:]
}

func joinWords(words []word) string {
	texts := make([]string, len(words))
	for i, w := range words {
		texts[i] = w.Text
	}
	return strings.Join(texts, " ")
}

// closers are trailing quote and bracket characters that follow the
// punctuation they close, e.g. the quote in `"Go home."`.
const closers = `"')]}»”’`

func lastPunctuation(text string) string {
	return strings.TrimRight(text, closers)
}

func endsSentence(text string) bool {
	t := lastPunctuation(text)
	return strings.HasSuffix(t, ".") && !strings.HasSuffix(t, "..") ||
		strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!")
}

func endsClause(text string) bool {
	t := lastPunctuation(text)
	return strings.HasSuffix(t, ",") || strings.HasSuffix(t, ";") ||
		strings.HasSuffix(t, ":") || strings.HasSuffix(t, "…") || strings.HasSuffix(t, "..") ||
		strings.HasSuffix(t, "—") || strings.HasSuffix(t, "-")
}

// layoutLines wraps words onto one line when they fit, otherwise onto two
// lines broken to balance the lengths, preferring a break after clause
// punctuation. ok is false when no wrapping keeps every line within
// maxLineChars; a lone over-long word is returned as is, with ok true.
func layoutLines(words []word) (lines []string, ok bool) {
	whole := joinWords(words)
	if utf8.RuneCountInString(whole) <= maxLineChars || len(words) == 1 {
		return []string{whole}, true
	}

	const clauseBonus = 8
	bestScore := -1
	best := -1
	for i := 1; i < len(words); i++ {
		first := utf8.RuneCountInString(joinWords(words[:i]))
		second := utf8.RuneCountInString(joinWords(words[i:]))
		if first > maxLineChars || second > maxLineChars {
			continue
		}
		score := 1000 - abs(first-second)
		if endsClause(words[i-1].Text) {
			score += clauseBonus
		}
		if score > bestScore {
			bestScore, best = score, i
		}
	}
	if best < 0 {
		return nil, false
	}
	return []string{joinWords(words[:best]), joinWords(words[best:])}, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// collapseRepeats drops a cue whose text equals the previous cue's, so a
// hallucinated stutter shows once.
func collapseRepeats(cues []cue) []cue {
	var out []cue
	for _, c := range cues {
		if n := len(out); n > 0 && strings.EqualFold(out[n-1].text(), c.text()) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// retime stretches each cue's end to satisfy the minimum duration and the
// maximum reading speed, never past the maximum cue duration or into the
// next cue.
func retime(cues []cue) []cue {
	for i := range cues {
		c := &cues[i]
		chars := utf8.RuneCountInString(c.text())
		need := max(minCueSeconds, float64(chars)/maxCharsPerSecond)
		target := min(c.Start+need, c.Start+maxCueSeconds)
		if i+1 < len(cues) {
			target = min(target, cues[i+1].Start)
		}
		c.End = max(c.End, target)
	}
	return cues
}

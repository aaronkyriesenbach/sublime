package whisper

import (
	"math"
	"strings"
	"time"
	"unicode"
)

// maxSeamDuplicate caps how many words a fixed cut's overlap can repeat.
const maxSeamDuplicate = 8

// seamDuplicateTolerance is how far apart two transcriptions of the same
// word can place it. Beyond that, equal words are a genuine repetition.
const seamDuplicateTolerance = seamOverlap / 2

// unit is the smallest piece of a segment whose timing is known: a word,
// or the whole segment when the sidecar returned no word timestamps.
type unit struct {
	text       string
	start, end float64
}

func (u unit) mid() float64 { return (u.start + u.end) / 2 }

func (s segment) units() []unit {
	if len(s.Words) == 0 {
		return []unit{{text: s.Text, start: s.Start, end: s.End}}
	}
	units := make([]unit, len(s.Words))
	for i, w := range s.Words {
		units[i] = unit{text: w.Text, start: w.Start, end: w.End}
	}
	return units
}

// offset returns the segment moved later by d, the start of its chunk.
func (s segment) offset(d time.Duration) segment {
	secs := d.Seconds()
	out := segment{Start: s.Start + secs, End: s.End + secs, Text: s.Text}
	for _, w := range s.Words {
		out.Words = append(out.Words, token{Text: w.Text, Start: w.Start + secs, End: w.End + secs})
	}
	return out
}

// keeping returns the segment reduced to the units for which keep is true,
// or false if none remain. A segment that keeps everything is returned
// untouched.
func (s segment) keeping(keep func(unit) bool) (segment, bool) {
	units := s.units()
	kept := units[:0:0]
	for _, u := range units {
		if keep(u) {
			kept = append(kept, u)
		}
	}
	switch len(kept) {
	case 0:
		return segment{}, false
	case len(units):
		return s, true
	}

	out := segment{Start: kept[0].start, End: kept[len(kept)-1].end}
	for _, u := range kept {
		out.Text += u.text
		out.Words = append(out.Words, token{Text: u.text, Start: u.start, End: u.end})
	}
	return out, true
}

// trim restricts segments to the units whose midpoint lies in [from, until);
// a nil bound leaves that side open.
func trim(segments []segment, from, until *time.Duration) []segment {
	var out []segment
	for _, s := range segments {
		if kept, ok := s.keeping(func(u unit) bool {
			return (from == nil || u.mid() >= from.Seconds()) && (until == nil || u.mid() < until.Seconds())
		}); ok {
			out = append(out, kept)
		}
	}
	return out
}

// dropRepeatedSeam removes from the head of next the words that already end
// the transcript so far, which a fixed cut's overlap hears twice. Words are
// compared by text ignoring case and punctuation, and must sit at nearly
// the same time, so a real repetition ("no, no") is left alone.
func dropRepeatedSeam(transcript, next []segment) []segment {
	var tail, head []unit
	for _, s := range transcript {
		tail = append(tail, s.units()...)
	}
	for _, s := range next {
		head = append(head, s.units()...)
	}

	for n := min(maxSeamDuplicate, len(tail), len(head)); n > 0; n-- {
		if sameWords(tail[len(tail)-n:], head[:n]) {
			return dropLeadingUnits(next, n)
		}
	}
	return next
}

func sameWords(a, b []unit) bool {
	for i := range a {
		if normalizeWord(a[i].text) == "" || normalizeWord(a[i].text) != normalizeWord(b[i].text) {
			return false
		}
		if math.Abs(a[i].mid()-b[i].mid()) > seamDuplicateTolerance.Seconds() {
			return false
		}
	}
	return true
}

// normalizeWord reduces text to lowercase letters and digits, so words are
// compared despite punctuation and case.
func normalizeWord(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

// dropLeadingUnits removes the first n units across segments.
func dropLeadingUnits(segments []segment, n int) []segment {
	var out []segment
	for _, s := range segments {
		skip := min(n, len(s.units()))
		n -= skip
		if skip == 0 {
			out = append(out, s)
			continue
		}
		seen := 0
		if kept, ok := s.keeping(func(unit) bool { seen++; return seen > skip }); ok {
			out = append(out, kept)
		}
	}
	return out
}

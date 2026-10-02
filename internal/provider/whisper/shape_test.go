package whisper_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// Limits the shaped SRT must respect; "about" in the ticket, so the reading
// speed gets a small tolerance for timestamp rounding.
const (
	wantMaxLines      = 2
	wantMaxLineChars  = 42
	wantMaxCPS        = 17.0 + 0.1
	wantMinCueSeconds = 1.0 - 0.001
	wantMaxCueSeconds = 7.0 + 0.001
)

type wireToken struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type wireSegment struct {
	Text  string      `json:"text"`
	Start float64     `json:"start"`
	End   float64     `json:"end"`
	Words []wireToken `json:"words"`
}

// say lays text out as word tokens starting at start, each word taking
// perWord seconds with the last tenth of that as silence before the next.
func say(start, perWord float64, text string) []wireToken {
	var tokens []wireToken
	for i, w := range strings.Fields(text) {
		s := start + float64(i)*perWord
		tokens = append(tokens, wireToken{Word: " " + w, Start: s, End: s + perWord*0.9})
	}
	return tokens
}

func concat(parts ...[]wireToken) []wireToken {
	var all []wireToken
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// segmentOf wraps tokens in one segment spanning them, as whisper would.
func segmentOf(tokens []wireToken) wireSegment {
	return wireSegment{Start: tokens[0].Start, End: tokens[len(tokens)-1].End, Words: tokens}
}

type srtCue struct {
	Start, End float64
	Lines      []string
}

func (c srtCue) text() string { return strings.Join(c.Lines, " ") }

var timingRE = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2}),(\d{3}) --> (\d{2}):(\d{2}):(\d{2}),(\d{3})$`)

func parseSRT(t *testing.T, srt string) []srtCue {
	t.Helper()
	var cues []srtCue
	for i, block := range strings.Split(strings.TrimSpace(srt), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 3 {
			t.Fatalf("cue block %d has %d lines, want an index, timing and text:\n%q", i+1, len(lines), block)
		}
		if lines[0] != strconv.Itoa(i+1) {
			t.Fatalf("cue %d has index line %q", i+1, lines[0])
		}
		m := timingRE.FindStringSubmatch(lines[1])
		if m == nil {
			t.Fatalf("cue %d has malformed timing line %q", i+1, lines[1])
		}
		at := func(o int) float64 {
			n := func(s string) float64 { v, _ := strconv.Atoi(s); return float64(v) }
			return n(m[o])*3600 + n(m[o+1])*60 + n(m[o+2]) + n(m[o+3])/1000
		}
		cues = append(cues, srtCue{Start: at(1), End: at(5), Lines: lines[2:]})
	}
	return cues
}

// downloadSRT runs Search and Download against a fake sidecar that answers
// with segments and returns the SRT.
func downloadSRT(t *testing.T, segments []wireSegment) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"segments": segments})
	if err != nil {
		t.Fatalf("encoding sidecar response: %v", err)
	}
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}
	p := newProvider(t, newFakeWhisperServer(t, body), audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	srt, err := p.Download(context.Background(), candidates[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	return string(srt)
}

// assertWellFormed checks the limits every shaped cue must respect.
func assertWellFormed(t *testing.T, cues []srtCue) {
	t.Helper()
	for i, c := range cues {
		if len(c.Lines) > wantMaxLines {
			t.Errorf("cue %d has %d lines, want at most %d: %q", i+1, len(c.Lines), wantMaxLines, c.Lines)
		}
		chars := utf8.RuneCountInString(c.text())
		for _, l := range c.Lines {
			if n := utf8.RuneCountInString(l); n > wantMaxLineChars {
				t.Errorf("cue %d line %q has %d chars, want at most %d", i+1, l, n, wantMaxLineChars)
			}
		}
		d := c.End - c.Start
		if d < wantMinCueSeconds || d > wantMaxCueSeconds {
			t.Errorf("cue %d lasts %.3fs, want between 1s and 7s: %q", i+1, d, c.Lines)
		}
		if cps := float64(chars) / d; cps > wantMaxCPS {
			t.Errorf("cue %d reads at %.1f chars/s, want at most 17: %q", i+1, cps, c.Lines)
		}
		if i > 0 && c.Start < cues[i-1].End-0.001 {
			t.Errorf("cue %d starts at %.3f before cue %d ends at %.3f", i+1, c.Start, i, cues[i-1].End)
		}
	}
}

func cueTexts(cues []srtCue) []string {
	texts := make([]string, len(cues))
	for i, c := range cues {
		texts[i] = c.text()
	}
	return texts
}

func TestDownload_ShapesWordsIntoWellFormedCues(t *testing.T) {
	tests := []struct {
		name     string
		segments []wireSegment
		want     []string // each cue's text with its lines joined by a space
	}{
		{
			name:     "short sentence stays on one line",
			segments: []wireSegment{segmentOf(say(1, 0.3, "Where did you go."))},
			want:     []string{"Where did you go."},
		},
		{
			name:     "sentence over one line wraps onto two lines of at most 42 characters",
			segments: []wireSegment{segmentOf(say(0, 0.3, "I think we should probably go back to the house now"))},
			want:     []string{"I think we should probably go back to the house now"},
		},
		{
			name: "sentence ends split cues",
			segments: []wireSegment{segmentOf(concat(
				say(0, 0.4, "This is the first sentence."),
				say(2.4, 0.4, "And this is the second one."),
			))},
			want: []string{"This is the first sentence.", "And this is the second one."},
		},
		{
			name: "a sentence too short to read is not split from the next",
			segments: []wireSegment{segmentOf(concat(
				say(0, 0.3, "Yes."),
				say(0.4, 0.3, "Of course."),
			))},
			want: []string{"Yes. Of course."},
		},
		{
			name: "long pause splits cues without punctuation",
			segments: []wireSegment{segmentOf(concat(
				say(0, 0.4, "we went home"),
				say(3, 0.4, "and slept"),
			))},
			want: []string{"we went home", "and slept"},
		},
		{
			name: "overflow breaks after a comma",
			segments: []wireSegment{segmentOf(say(0, 0.4,
				"When the storm finally passed over the quiet little harbor town, the fishermen went back to their boats and nets"))},
			want: []string{
				"When the storm finally passed over the quiet little harbor town,",
				"the fishermen went back to their boats and nets",
			},
		},
		{
			name:     "cue longer than seven seconds is split",
			segments: []wireSegment{segmentOf(say(0, 0.6, "an ox is at it on me we do go so no up us be by"))},
			want:     []string{"an ox is at it on me we do go so", "no up us be by"},
		},
		{
			name:     "fast speech is held on screen long enough to read",
			segments: []wireSegment{segmentOf(concat(say(0, 0.25, "extraordinarily complicated administrative responsibilities"), say(5, 0.4, "Fine.")))},
			want:     []string{"extraordinarily complicated administrative responsibilities", "Fine."},
		},
		{
			name: "a blip is held for the minimum duration",
			segments: []wireSegment{segmentOf(concat(
				say(0, 0.2, "Hm."),
				say(5, 0.4, "Okay then."),
			))},
			want: []string{"Hm.", "Okay then."},
		},
		{
			name: "runs of identical cues close in time collapse",
			segments: []wireSegment{segmentOf(concat(
				say(0, 0.6, "Thank you."),
				say(1.5, 0.6, "Thank you."),
				say(3, 0.6, "Thank you."),
				say(9, 0.5, "See you soon."),
				say(12, 0.5, "Thank you."),
			))},
			want: []string{"Thank you.", "See you soon.", "Thank you."},
		},
		{
			name: "identical cues far apart in time are both kept",
			segments: []wireSegment{segmentOf(concat(
				say(0, 0.5, "What?"),
				say(5, 0.5, "What?"),
			))},
			want: []string{"What?", "What?"},
		},
		{
			name: "sub-word tokens merge into words and punctuation attaches to its word",
			segments: []wireSegment{segmentOf([]wireToken{
				{Word: " Gener", Start: 0, End: 0.3},
				{Word: "al", Start: 0.3, End: 0.5},
				{Word: " Ken", Start: 0.6, End: 0.8},
				{Word: "obi", Start: 0.8, End: 1.0},
				{Word: ",", Start: 1.0, End: 1.1},
				{Word: " you", Start: 1.2, End: 1.4},
				{Word: " are", Start: 1.4, End: 1.6},
				{Word: " bold", Start: 1.6, End: 1.9},
				{Word: "!", Start: 1.9, End: 2.0},
			})},
			want: []string{"General Kenobi, you are bold!"},
		},
		{
			name: "a word split across segments is merged",
			segments: []wireSegment{
				segmentOf([]wireToken{{Word: " Gener", Start: 0, End: 0.4}}),
				segmentOf([]wireToken{{Word: "al", Start: 0.4, End: 0.7}, {Word: " Grievous", Start: 0.8, End: 1.5}}),
			},
			want: []string{"General Grievous"},
		},
		{
			name: "control tokens and blank segments contribute nothing",
			segments: []wireSegment{
				segmentOf([]wireToken{{Word: "[_BEG_]", Start: 0, End: 0}, {Word: " Hello", Start: 0, End: 0.5}, {Word: " world.", Start: 0.5, End: 1.2}}),
				{Text: "   ", Start: 3, End: 4},
			},
			want: []string{"Hello world."},
		},
		{
			name:     "a segment without word timings is spread over its span",
			segments: []wireSegment{{Text: " Hello there my friend.", Start: 2, End: 5}},
			want:     []string{"Hello there my friend."},
		},
		{
			name:     "a stretched final word does not stretch its cue",
			segments: []wireSegment{{Words: []wireToken{{Word: " Goodbye.", Start: 3, End: 3600}}, Start: 3, End: 3600}},
			want:     []string{"Goodbye."},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cues := parseSRT(t, downloadSRT(t, tc.segments))

			assertWellFormed(t, cues)
			got := cueTexts(cues)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("cue texts =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestDownload_PreservesWordTimingAtCueEdges(t *testing.T) {
	cues := parseSRT(t, downloadSRT(t, []wireSegment{
		segmentOf(concat(say(10, 0.4, "First thing here."), say(20, 0.4, "Second thing there."))),
	}))

	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}
	for i, wantStart := range []float64{10, 20} {
		if cues[i].Start != wantStart {
			t.Errorf("cue %d starts at %.3f, want %.3f", i+1, cues[i].Start, wantStart)
		}
	}
}

func TestDownload_WrapsAtBalancedLineBreakAndPrefersClauseBreak(t *testing.T) {
	cues := parseSRT(t, downloadSRT(t, []wireSegment{
		segmentOf(say(0, 0.3, "If you want to leave, you can leave right now")),
	}))

	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1: %+v", len(cues), cues)
	}
	want := []string{"If you want to leave,", "you can leave right now"}
	if strings.Join(cues[0].Lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", cues[0].Lines, want)
	}
}

func TestDownload_OutputTakesAMarkerFromTheSRTCodec(t *testing.T) {
	srt := downloadSRT(t, []wireSegment{segmentOf(say(0, 0.4, "Hello there, how are you today?"))})
	codec, ok := marker.CodecFor(".srt")
	if !ok {
		t.Fatal("no .srt marker codec registered")
	}
	want := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	marked, err := codec.Write([]byte(srt), want)
	if err != nil {
		t.Fatalf("marker Write: %v", err)
	}
	got, presence := codec.Read(marked)
	if presence != marker.Present || got != want {
		t.Errorf("marker Read = %+v, %v; want %+v present", got, presence, want)
	}
	if !strings.HasPrefix(string(marked), strings.TrimRight(srt, "\n")) {
		t.Errorf("marker codec altered the cues:\n%s", marked)
	}
}

func TestDownload_LongTranscriptKeepsEveryCueWithinLimits(t *testing.T) {
	sentences := []string{
		"I never thought I would see this place again after everything that happened.",
		"Well, here we are.",
		"You said you would wait for me at the station, but you were nowhere to be found!",
		"What was I supposed to do?",
		"Nothing, apparently, because nobody ever tells me anything around here until it is far too late",
	}
	var tokens []wireToken
	at := 0.0
	for round := 0; round < 4; round++ {
		for i, s := range sentences {
			n := len(strings.Fields(s))
			tokens = append(tokens, say(at, 0.5, fmt.Sprintf("%d %s", round*10+i, s))...)
			at += float64(n+1)*0.5 + 0.2
		}
	}

	cues := parseSRT(t, downloadSRT(t, []wireSegment{segmentOf(tokens)}))

	assertWellFormed(t, cues)
	if len(cues) < len(sentences) {
		t.Errorf("got %d cues for %d sentences, want at least one per sentence", len(cues), len(sentences))
	}
}

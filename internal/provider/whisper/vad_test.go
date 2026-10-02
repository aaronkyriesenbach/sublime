package whisper_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

// skewedSegment is a segment as a --vad sidecar reports it: its start and
// end are on the original timeline while its words stay shortenedBy seconds
// early (whisper.cpp#3174).
func skewedSegment(start float64, shortenedBy float64, text string) wireSegment {
	tokens := say(start-shortenedBy, 0.4, text)
	last := tokens[len(tokens)-1]
	return wireSegment{Start: start, End: start + (last.End - tokens[0].Start), Text: " " + text, Words: tokens}
}

func TestDownload_PlacesWordsAtSegmentStartWhenSidecarWordTimesAreSkewed(t *testing.T) {
	tests := []struct {
		name     string
		segments []wireSegment
		want     []srtCue
	}{
		{
			name:     "first segment's words at zero",
			segments: []wireSegment{skewedSegment(100.5, 100.5, "Hello there my friend")},
			want:     []srtCue{{Start: 100.5, End: 102.5, Lines: []string{"Hello there my friend"}}},
		},
		{
			name: "skew growing with each removed stretch of silence",
			segments: []wireSegment{
				skewedSegment(20, 20, "First line here"),
				skewedSegment(60, 57, "Second line here"),
				skewedSegment(155, 97.5, "Third line here"),
			},
			want: []srtCue{
				{Start: 20, End: 21.2, Lines: []string{"First line here"}},
				{Start: 60, End: 61.2, Lines: []string{"Second line here"}},
				{Start: 155, End: 156.2, Lines: []string{"Third line here"}},
			},
		},
		{
			name:     "words already aligned stay put",
			segments: []wireSegment{skewedSegment(30, 0, "No skew at all")},
			want:     []srtCue{{Start: 30, End: 31.2, Lines: []string{"No skew at all"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSRT(t, downloadSRT(t, tc.segments))

			if len(got) != len(tc.want) {
				t.Fatalf("cues = %+v, want %d cues like %+v", got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if math.Abs(got[i].Start-w.Start) > 0.01 {
					t.Errorf("cue %d starts at %.3f, want %.3f", i+1, got[i].Start, w.Start)
				}
				if got[i].text() != w.text() {
					t.Errorf("cue %d text = %q, want %q", i+1, got[i].text(), w.text())
				}
			}
		})
	}
}

func TestDownload_RealignsSkewedWordsBeforeOffsettingByChunkStart(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	skewed := func(start, shortenedBy float64, text string) []byte {
		body, err := json.Marshal(map[string]any{"segments": []wireSegment{skewedSegment(start, shortenedBy, text)}})
		if err != nil {
			t.Fatalf("encoding sidecar response: %v", err)
		}
		return body
	}
	server.chunkBodies = [][]byte{
		skewed(40, 40, "first chunk"),
		skewed(70, 55, "second chunk"),
		skewed(5, 5, "third chunk"),
	}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Chunks start at 0:00, 9:59 and 19:59 (cut at the target with a 1 s
	// overlap), so the later two land at 599+70 and 1199+5 seconds.
	want := fmt.Sprint([][2]string{
		{"00:00:40,000", "first chunk"},
		{"00:11:09,000", "second chunk"},
		{"00:20:04,000", "third chunk"},
	})
	if got := fmt.Sprint(cues(t, srt)); got != want {
		t.Errorf("cues = %s, want %s", got, want)
	}
}

func TestDownload_AsksSidecarForNoLanguageProbabilities(t *testing.T) {
	server := newFakeWhisperServer(t, sampleResponse(t))
	audio := longVideo(at(25, 0))
	server.chunkBodies = [][]byte{oneWord("a", 1), oneWord("b", 1), oneWord("c", 1)}
	p := newProvider(t, server, audio, nil)

	if _, err := download(t, p); err != nil {
		t.Fatalf("Download: %v", err)
	}

	reqs := server.Requests()
	if len(reqs) != 3 {
		t.Fatalf("sidecar saw %d requests, want 3", len(reqs))
	}
	for i, req := range reqs {
		if got := req.Fields["no_language_probabilities"]; got != "true" {
			t.Errorf("request %d no_language_probabilities = %q, want %q so a speechless chunk is not a 500", i+1, got, "true")
		}
	}
}

func TestDownload_AsksSidecarNotToCarryTextContextBetweenWindows(t *testing.T) {
	server := newFakeWhisperServer(t, sampleResponse(t))
	p := newProvider(t, server, longVideo(at(5, 0)), nil)

	if _, err := download(t, p); err != nil {
		t.Fatalf("Download: %v", err)
	}

	reqs := server.Requests()
	if len(reqs) == 0 {
		t.Fatal("sidecar saw no requests")
	}
	for i, req := range reqs {
		if got := req.Fields["max_context"]; got != "0" {
			t.Errorf("request %d max_context = %q, want %q so one hallucination cannot repeat across windows", i+1, got, "0")
		}
	}
}

func TestDownload_SpeechlessChunkAnsweredWithEmptySegmentsIsNotAFailure(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	speechless := []byte(`{"task":"transcribe","language":"english","duration":600.0,"text":"","segments":[]}`)
	server.chunkBodies = [][]byte{oneWord("hello", 1), speechless, speechless, speechless, oneWord("bye", 1)}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := len(cues(t, srt)); got != 2 {
		t.Errorf("got %d cues, want the 2 words around the wordless chunk:\n%s", got, srt)
	}
	if got := len(server.Requests()); got != 5 {
		t.Errorf("sidecar saw %d requests, want 5 (the wordless chunk attempted 3 times)", got)
	}
}

package whisper_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider/whisper"
)

// at builds a duration from minutes and seconds into the video.
func at(minutes, seconds float64) time.Duration {
	return time.Duration((minutes*60 + seconds) * float64(time.Second))
}

// word is one word in a canned transcript, in seconds from the start of the
// audio the chunk's request carried.
type word struct {
	text       string
	start, end float64
}

// transcript renders one chunk's canned verbose_json response with one
// segment holding the given words.
func transcript(words ...word) []byte {
	var text, wordsJSON []string
	for _, w := range words {
		text = append(text, w.text)
		wordsJSON = append(wordsJSON, fmt.Sprintf(`{"word": " %s", "start": %v, "end": %v}`, w.text, w.start, w.end))
	}
	return []byte(fmt.Sprintf(`{"segments": [{"text": " %s", "start": %v, "end": %v, "words": [%s]}]}`,
		strings.Join(text, " "), words[0].start, words[len(words)-1].end, strings.Join(wordsJSON, ",")))
}

// oneWord is a chunk transcript of a single word at the given second.
func oneWord(text string, second float64) []byte {
	return transcript(word{text, second, second + 0.5})
}

// longVideo is a fake audio source for a video of the given length whose
// only audio stream is English.
func longVideo(length time.Duration, silences ...audiosource.Silence) *audiosource.FakeSource {
	return &audiosource.FakeSource{
		Streams:          []audiosource.Stream{{Index: 1, Language: "eng"}},
		Length:           length,
		SilenceIntervals: silences,
	}
}

func download(t *testing.T, p *whisper.Provider) ([]byte, error) {
	t.Helper()
	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	return p.Download(context.Background(), candidates[0])
}

// cues parses SRT into (start timestamp, text) pairs.
func cues(t *testing.T, srt []byte) [][2]string {
	t.Helper()
	var out [][2]string
	for _, block := range strings.Split(strings.TrimSpace(string(srt)), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 3 {
			t.Fatalf("malformed SRT block %q in\n%s", block, srt)
		}
		start, _, _ := strings.Cut(lines[1], " --> ")
		out = append(out, [2]string{start, strings.Join(lines[2:], " ")})
	}
	return out
}

func TestDownload_ShortFileIsOneChunk(t *testing.T) {
	audio := longVideo(testChunkLength)
	server := newFakeWhisperServer(t, oneWord("hello", 1))
	p := newProvider(t, server, audio, nil)

	if _, err := download(t, p); err != nil {
		t.Fatalf("Download: %v", err)
	}

	if len(audio.ExtractCalls) != 1 || audio.ExtractCalls[0].Range != (audiosource.Range{}) {
		t.Errorf("Extract calls = %+v, want one call for the whole stream", audio.ExtractCalls)
	}
	if got := len(server.Requests()); got != 1 {
		t.Errorf("sidecar saw %d requests, want 1", got)
	}
}

func TestDownload_CutsAtSilenceNearTargetAndOffsetsTimestampsExactly(t *testing.T) {
	audio := longVideo(at(25, 0),
		// Too short to beat the long silence in the same window.
		audiosource.Silence{Start: at(9, 0), End: at(9, 1)},
		audiosource.Silence{Start: at(9, 50), End: at(9, 52)},
		// The longest silence of all lies far outside the window around 10 minutes.
		audiosource.Silence{Start: at(3, 0), End: at(3, 30)},
		audiosource.Silence{Start: at(19, 30), End: at(19, 34)},
	)
	server := newFakeWhisperServer(t, nil)
	server.chunkBodies = [][]byte{oneWord("first", 1), oneWord("second", 1), oneWord("third", 1)}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	wantRanges := []audiosource.Range{
		{Start: 0, Duration: at(9, 51)},
		{Start: at(9, 51), Duration: at(9, 41)},
		{Start: at(19, 32)},
	}
	if len(audio.ExtractCalls) != len(wantRanges) {
		t.Fatalf("Extract calls = %+v, want %d chunks", audio.ExtractCalls, len(wantRanges))
	}
	for i, want := range wantRanges {
		if got := audio.ExtractCalls[i].Range; got != want {
			t.Errorf("chunk %d range = %+v, want %+v", i+1, got, want)
		}
		if audio.ExtractCalls[i].StreamIndex != 1 {
			t.Errorf("chunk %d stream = %d, want 1", i+1, audio.ExtractCalls[i].StreamIndex)
		}
	}

	want := [][2]string{
		{"00:00:01,000", "first"},
		{"00:09:52,000", "second"},
		{"00:19:33,000", "third"},
	}
	got := cues(t, srt)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("cues = %v, want %v", got, want)
	}
}

func TestDownload_WithoutSilenceCutsAtTargetWithOverlapAndDropsDuplicatedWords(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	// The sentence "see you at noon" straddles the 10:00 cut; both chunks
	// hear all of it, and the second chunk (which starts at 9:59) also hears
	// "ok" after it. Times are seconds into each chunk's own audio.
	server.chunkBodies = [][]byte{
		transcript(
			word{"hello", 596, 596.5},
			word{"see", 599.0, 599.4}, word{"you", 599.6, 600.0},
			word{"at", 600.2, 600.5}, word{"noon", 600.7, 601.0}),
		transcript(
			word{"see", 0, 0.4}, word{"you", 0.6, 1.0},
			word{"at", 1.2, 1.5}, word{"noon", 1.7, 2.0},
			word{"ok", 3.0, 3.3}),
		oneWord("later", 5),
	}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	wantRanges := []audiosource.Range{
		{Start: 0, Duration: at(10, 1)},
		{Start: at(9, 59), Duration: at(10, 2)},
		{Start: at(19, 59)},
	}
	for i, want := range wantRanges {
		if got := audio.ExtractCalls[i].Range; got != want {
			t.Errorf("chunk %d range = %+v, want %+v", i+1, got, want)
		}
	}

	var texts []string
	for _, c := range cues(t, srt) {
		texts = append(texts, c[1])
	}
	if got, want := strings.Join(texts, " "), "hello see you at noon ok later"; got != want {
		t.Errorf("transcript = %q, want %q (each word once)", got, want)
	}
	// "noon" is first heard by the second chunk at 1.7 s into audio that
	// starts at 599 s, which must land at exactly 600.7 s.
	if got := cues(t, srt)[1]; got[0] != "00:10:00,700" || got[1] != "noon ok" {
		t.Errorf("second cue = %v, want it to start at 00:10:00,700 with %q", got, "noon ok")
	}
}

func TestDownload_LogsProgressPerChunk(t *testing.T) {
	audio := longVideo(at(25, 0), audiosource.Silence{Start: at(9, 50), End: at(9, 52)}, audiosource.Silence{Start: at(19, 30), End: at(19, 34)})
	var logs bytes.Buffer
	p := newProvider(t, newFakeWhisperServer(t, oneWord("hi", 1)), audio, &logs)

	if _, err := download(t, p); err != nil {
		t.Fatalf("Download: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if want := fmt.Sprintf("chunk %d of 3", i); !strings.Contains(logs.String(), want) {
			t.Errorf("log does not contain %q:\n%s", want, logs.String())
		}
	}
}

func TestDownload_ChunkLengthIsConfigurable(t *testing.T) {
	audio := longVideo(at(3, 0), audiosource.Silence{Start: at(0, 59), End: at(1, 1)})
	server := newFakeWhisperServer(t, oneWord("hi", 1))
	p, err := whisper.New(whisper.Config{Endpoint: server.URL, Audio: audio, ChunkLength: time.Minute})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}

	if _, err := download(t, p); err != nil {
		t.Fatalf("Download: %v", err)
	}
	// Cut at the silence near 1:00, then fixed cuts at the target.
	if got := len(audio.ExtractCalls); got != 3 {
		t.Fatalf("Extract called %d times, want 3 chunks for 3 minutes at 1-minute chunks: %+v", got, audio.ExtractCalls)
	}
	if got := audio.ExtractCalls[0].Range; got != (audiosource.Range{Duration: at(1, 0)}) {
		t.Errorf("first chunk range = %+v, want it to end at the silence", got)
	}
}

func TestDownload_FailingChunkFailsTheWholeSubtitle(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	server.chunkBodies = [][]byte{oneWord("first", 1), []byte(`{"error":"inference failed"}`)}
	server.status = http.StatusOK
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err == nil {
		t.Fatalf("Download returned a subtitle despite a failed chunk:\n%s", srt)
	}
	if !strings.Contains(err.Error(), "chunk 2 of 3") {
		t.Errorf("error %q does not say which chunk failed", err)
	}
}

func TestDownload_SilenceDetectionFailureIsAnError(t *testing.T) {
	audio := longVideo(at(25, 0))
	audio.SilencesErr = errors.New("ffmpeg exploded")
	p := newProvider(t, newFakeWhisperServer(t, oneWord("hi", 1)), audio, nil)

	if _, err := download(t, p); !errors.Is(err, audio.SilencesErr) {
		t.Fatalf("Download error = %v, want it to wrap %v", err, audio.SilencesErr)
	}
}

func TestDownload_CancellingTheContextAbortsTheInFlightRequest(t *testing.T) {
	audio := longVideo(at(5, 0))
	server := newFakeWhisperServer(t, nil)
	server.hang = true
	p := newProvider(t, server, audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := p.Download(ctx, domain.Candidate{ID: candidates[0].ID})
		result <- err
	}()

	select {
	case <-server.hung:
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar never received the request")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Download error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Download did not return after the context was cancelled")
	}
	select {
	case <-server.gone:
	case <-time.After(5 * time.Second):
		t.Error("sidecar's connection was never closed, so it could not abort inference")
	}
}

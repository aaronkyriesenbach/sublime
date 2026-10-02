package whisper_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/provider"
)

// repeatedLine is a chunk's canned response in which the same line is
// decoded count times in a row, the way a whisper decoding loop looks.
func repeatedLine(text string, count int) []byte {
	segments := make([]string, count)
	for i := range segments {
		start := float64(i) * 2
		segments[i] = fmt.Sprintf(`{"text": " %s", "start": %v, "end": %v, "words": [{"word": " %s", "start": %v, "end": %v}]}`,
			text, start, start+1.5, text, start, start+1.5)
	}
	return []byte(`{"segments": [` + strings.Join(segments, ",") + `]}`)
}

// temperatures lists the temperature each recorded transcription request
// asked for; "" means the request left the sidecar's default.
func temperatures(server *fakeWhisperServer) []string {
	var got []string
	for _, req := range server.Requests() {
		got = append(got, req.Fields["temperature"])
	}
	return got
}

func assertMiss(t *testing.T, err error) {
	t.Helper()
	var miss *provider.MissError
	if !errors.As(err, &miss) {
		t.Fatalf("Download error = %v, want a *provider.MissError", err)
	}
}

func TestDownload_LoopingChunkIsRetriedAtRaisedTemperature(t *testing.T) {
	audio := longVideo(at(5, 0))
	server := newFakeWhisperServer(t, nil)
	server.chunkBodies = [][]byte{
		repeatedLine("Thank you.", 12),
		oneWord("hello", 1),
	}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if got, want := fmt.Sprint(cues(t, srt)), fmt.Sprint([][2]string{{"00:00:01,000", "hello"}}); got != want {
		t.Errorf("cues = %s, want only the retry's output %s", got, want)
	}
	if got, want := fmt.Sprint(temperatures(server)), fmt.Sprint([]string{"", "0.4"}); got != want {
		t.Errorf("request temperatures = %s, want %s", got, want)
	}
	if got := len(audio.ExtractCalls); got != 1 {
		t.Errorf("Extract called %d times, want the chunk's audio extracted once", got)
	}
}

func TestDownload_FailedChunkRequestIsRetriedAtRaisedTemperature(t *testing.T) {
	server := newFakeWhisperServer(t, nil)
	server.chunkBodies = [][]byte{[]byte(`{"error":"inference failed"}`), []byte(`{"error":"inference failed"}`), oneWord("hello", 1)}
	p := newProvider(t, server, longVideo(at(5, 0)), nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if got := cues(t, srt); len(got) != 1 || got[0][1] != "hello" {
		t.Errorf("cues = %v, want the third attempt's output", got)
	}
	if got, want := fmt.Sprint(temperatures(server)), fmt.Sprint([]string{"", "0.4", "0.8"}); got != want {
		t.Errorf("request temperatures = %s, want %s", got, want)
	}
}

func TestDownload_UnrecoverableChunkIsAWholePairMissWithNoSubtitle(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	loop := repeatedLine("Thank you.", 12)
	server.chunkBodies = [][]byte{oneWord("first", 1), loop, loop, loop, oneWord("third", 1)}
	var logs bytes.Buffer
	p := newProvider(t, server, audio, &logs)

	srt, err := download(t, p)

	if err == nil {
		t.Fatalf("Download returned a partial subtitle:\n%s", srt)
	}
	assertMiss(t, err)
	if !strings.Contains(err.Error(), "chunk 2 of 3") {
		t.Errorf("miss %q does not say which chunk was unrecoverable", err)
	}
	if got := len(server.Requests()); got != 4 {
		t.Errorf("sidecar saw %d requests, want 4 (chunk 1, then 3 attempts at chunk 2, then no more)", got)
	}
	if !strings.Contains(logs.String(), "repeated line") {
		t.Errorf("log does not carry the cause of the miss:\n%s", logs.String())
	}
}

func TestDownload_LoopConfinedToOneChunkLeavesOtherChunksUntouched(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	server.chunkBodies = [][]byte{
		oneWord("first", 1),
		repeatedLine("la la la", 30),
		oneWord("second", 1),
		oneWord("third", 1),
	}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	var texts []string
	for _, c := range cues(t, srt) {
		texts = append(texts, c[1])
	}
	if got, want := strings.Join(texts, " "), "first second third"; got != want {
		t.Errorf("transcript = %q, want %q with no trace of the loop", got, want)
	}
}

func TestDownload_LineDominatingAChunkIsRejectedEvenWhenNotConsecutive(t *testing.T) {
	var segments []string
	for i := range 12 {
		text := "Thank you."
		if i%4 == 3 {
			text = fmt.Sprintf("Line %c", 'a'+i)
		}
		segments = append(segments, fmt.Sprintf(`{"text": " %s", "start": %d, "end": %d}`, text, i*2, i*2+1))
	}
	dominated := []byte(`{"segments": [` + strings.Join(segments, ",") + `]}`)
	server := newFakeWhisperServer(t, dominated)
	p := newProvider(t, server, longVideo(at(5, 0)), nil)

	_, err := download(t, p)

	assertMiss(t, err)
	if got := len(server.Requests()); got != 3 {
		t.Errorf("sidecar saw %d requests, want 3 attempts", got)
	}
}

func TestDownload_EmptyFinalAttemptAfterALoopIsAMissNotNoSpeech(t *testing.T) {
	server := newFakeWhisperServer(t, nil)
	loop := repeatedLine("Thank you.", 12)
	server.chunkBodies = [][]byte{loop, loop, []byte(`{"segments":[]}`)}
	p := newProvider(t, server, longVideo(at(5, 0)), nil)

	srt, err := download(t, p)

	if err == nil {
		t.Fatalf("Download shipped a subtitle with a hallucinating chunk silently dropped:\n%s", srt)
	}
	assertMiss(t, err)
}

func TestDownload_RequestFailureAfterALoopIsAMissNotARetrievalFailure(t *testing.T) {
	server := newFakeWhisperServer(t, nil)
	loop := repeatedLine("Thank you.", 12)
	server.chunkBodies = [][]byte{loop, []byte(`{"error":"inference failed"}`), []byte(`{"error":"inference failed"}`)}
	p := newProvider(t, server, longVideo(at(5, 0)), nil)

	_, err := download(t, p)

	assertMiss(t, err)
}

func TestDownload_ChunkThatStaysEmptyIsAcceptedAsWordless(t *testing.T) {
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	empty := []byte(`{"segments":[]}`)
	long := make([]word, 0, 80)
	for i := range 80 {
		long = append(long, word{fmt.Sprintf("w%d", i), float64(i), float64(i) + 0.5})
	}
	server.chunkBodies = [][]byte{transcript(long...), empty, empty, empty, transcript(long...)}
	p := newProvider(t, server, audio, nil)

	srt, err := download(t, p)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if len(cues(t, srt)) == 0 {
		t.Error("subtitle has no cues, want the speech of the chunks around the wordless one")
	}
	if got := len(server.Requests()); got != 5 {
		t.Errorf("sidecar saw %d requests, want 5 (empty chunk attempted 3 times)", got)
	}
}

func TestDownload_NearEmptyTranscriptForALongRuntimeIsAMiss(t *testing.T) {
	var logs bytes.Buffer
	server := newFakeWhisperServer(t, []byte(`{"segments":[]}`))
	server.chunkBodies = [][]byte{oneWord("hmm", 1)}
	p := newProvider(t, server, longVideo(at(100, 0)), &logs)

	srt, err := download(t, p)

	if err == nil {
		t.Fatalf("Download returned a subtitle for 1 word in 100 minutes:\n%s", srt)
	}
	assertMiss(t, err)
	if !strings.Contains(logs.String(), "only 1 words") {
		t.Errorf("log does not carry the cause of the miss:\n%s", logs.String())
	}
}

func TestDownload_RepeatedLineDominatingTheWholeTranscriptIsAMiss(t *testing.T) {
	// Each chunk holds too few repeats to be a loop on its own; together
	// they are one repeated line across the film.
	audio := longVideo(at(25, 0))
	server := newFakeWhisperServer(t, nil)
	server.chunkBodies = [][]byte{repeatedLine("Thank you.", 6), repeatedLine("Thank you.", 6), repeatedLine("Thank you.", 6)}
	p := newProvider(t, server, audio, nil)

	_, err := download(t, p)

	assertMiss(t, err)
}

func TestDownload_ChunkRequestThatKeepsFailingIsARetrievalFailureNotAMiss(t *testing.T) {
	failure := []byte(`{"error":"inference failed"}`)
	server := newFakeWhisperServer(t, failure)
	p := newProvider(t, server, longVideo(at(5, 0)), nil)

	_, err := download(t, p)

	if err == nil {
		t.Fatal("Download: want an error, got nil")
	}
	var miss *provider.MissError
	if errors.As(err, &miss) {
		t.Errorf("Download error = %v: a failing sidecar must not read as a hallucinating one", err)
	}
	if got := len(server.Requests()); got != 3 {
		t.Errorf("sidecar saw %d requests, want 3 attempts", got)
	}
}

package pipeline_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider/whisper"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

// tonesChunkLength is the chunk length the tone fixture is built for: see
// the video/tones.mp4 comment in testdata/integration/generate.sh.
const tonesChunkLength = 10 * time.Second

// tonesCuts are the middles of the fixture's silences nearest each chunk
// target: the 9-10s, 19.5-20.5s and 29-30s silences. The shorter 11-11.6s
// silence also lies in the first cut window and must lose to 9-10s.
var tonesCuts = []time.Duration{9500 * time.Millisecond, 20 * time.Second, 29500 * time.Millisecond}

// tonesDuration is the fixture's length.
const tonesDuration = 34 * time.Second

// cutTolerance absorbs silencedetect's and the AAC encoder's timing slack.
const cutTolerance = 50 * time.Millisecond

// edgeWindow is how much of a cut chunk's edge must be silent. The
// fixture's silences are at least 1s wide, so the cut's neighbourhood is
// quiet even allowing for the tolerance.
const edgeWindow = 250 * time.Millisecond

// Tone amplitude is 0.5 of full scale; anything below silentPeak counts as
// silence (encoder noise), anything above tonePeak as the tone.
const (
	silentPeak = 1000
	tonePeak   = 10000
)

func requireFFmpegBinaries(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary not found on PATH; skipping real-audio-source integration test", bin)
		}
	}
}

// chunkTranscripts is what the fake whisper server says about each chunk,
// in request order. Timestamps are relative to the chunk's own audio, as
// whisper.cpp reports them.
var chunkTranscripts = []string{
	`{"segments":[{"start":1.0,"end":2.5,"text":" Chunk one speaks.","words":[` +
		`{"word":" Chunk","start":1.0,"end":1.4},{"word":" one","start":1.4,"end":1.9},{"word":" speaks.","start":1.9,"end":2.5}]}]}`,
	`{"segments":[{"start":1.0,"end":2.5,"text":" Chunk two speaks.","words":[` +
		`{"word":" Chunk","start":1.0,"end":1.4},{"word":" two","start":1.4,"end":1.9},{"word":" speaks.","start":1.9,"end":2.5}]}]}`,
	`{"segments":[{"start":1.0,"end":2.5,"text":" Chunk three speaks.","words":[` +
		`{"word":" Chunk","start":1.0,"end":1.4},{"word":" three","start":1.4,"end":1.9},{"word":" speaks.","start":1.9,"end":2.5}]}]}`,
	`{"segments":[{"start":1.0,"end":2.5,"text":" Chunk four speaks.","words":[` +
		`{"word":" Chunk","start":1.0,"end":1.4},{"word":" four","start":1.4,"end":1.9},{"word":" speaks.","start":1.9,"end":2.5}]}]}`,
}

// chunkSpeechStart is where each fake transcript's speech begins within its chunk.
const chunkSpeechStart = time.Second

// recordingWhisperServer answers the n-th /inference request with the n-th
// canned transcript and keeps each request's audio for inspection.
type recordingWhisperServer struct {
	*httptest.Server

	mu     sync.Mutex
	chunks [][]byte
}

func newRecordingWhisperServer(t *testing.T) *recordingWhisperServer {
	t.Helper()
	s := &recordingWhisperServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		audio := make([]byte, 0, 1<<20)
		buf := make([]byte, 32<<10)
		for {
			n, readErr := file.Read(buf)
			audio = append(audio, buf[:n]...)
			if readErr != nil {
				break
			}
		}

		s.mu.Lock()
		n := len(s.chunks)
		s.chunks = append(s.chunks, audio)
		s.mu.Unlock()

		if n >= len(chunkTranscripts) {
			http.Error(w, `{"error":"unexpected extra chunk"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chunkTranscripts[n]))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingWhisperServer) Chunks() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.chunks...)
}

// pcmSamples decodes the payload of the canonical 44-byte-header WAV the
// audio source produces.
func pcmSamples(t *testing.T, wav []byte) []int16 {
	t.Helper()
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" {
		t.Fatalf("chunk is not a WAV file (%d bytes)", len(wav))
	}
	samples := make([]int16, (len(wav)-44)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(wav[44+2*i:]))
	}
	return samples
}

func peak(samples []int16) int {
	peak := 0
	for _, s := range samples {
		peak = max(peak, int(s), -int(s))
	}
	return peak
}

func withinTolerance(got, want time.Duration) bool {
	diff := got - want
	return diff >= -cutTolerance && diff <= cutTolerance
}

var srtTimingRE = regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2}),(\d{3}) --> (\d{2}):(\d{2}):(\d{2}),(\d{3})\n([^\n]*)`)

// srtCue is a parsed cue's start time and first text line.
type srtCue struct {
	start time.Duration
	text  string
}

// parseGeneratedCues returns the SRT's cues in order, leaving out the
// Marker cue Sublime embeds.
func parseGeneratedCues(t *testing.T, srt []byte) []srtCue {
	t.Helper()
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("parsing SRT number %q: %v", s, err)
		}
		return n
	}
	var cues []srtCue
	for _, m := range srtTimingRE.FindAllStringSubmatch(string(srt), -1) {
		if strings.HasPrefix(m[9], marker.Prefix) {
			continue
		}
		start := time.Duration(atoi(m[1]))*time.Hour + time.Duration(atoi(m[2]))*time.Minute +
			time.Duration(atoi(m[3]))*time.Second + time.Duration(atoi(m[4]))*time.Millisecond
		cues = append(cues, srtCue{start: start, text: m[9]})
	}
	return cues
}

// TestIntegration_RealAudioSource_WhisperChunksAtSilences drives the real
// ffprobe/ffmpeg audio source and the whisper Provider through the real
// pipeline against a fake whisper server and a video whose audio is tone
// bursts separated by silences. It proves, without a model, that chunks are
// cut inside the silences and that each chunk's timestamps are offset to
// its exact start in the final SRT.
func TestIntegration_RealAudioSource_WhisperChunksAtSilences(t *testing.T) {
	requireFFmpegBinaries(t)

	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.BluRay.x264-TESTGROUP.mp4")
	videoContent, err := os.ReadFile(filepath.Join(integrationVideoDir, "tones.mp4"))
	if err != nil {
		t.Fatalf("reading tone fixture: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	server := newRecordingWhisperServer(t)
	whisperProvider, err := whisper.New(whisper.Config{
		Endpoint:    server.URL,
		Audio:       audiosource.NewFFSource(),
		ChunkLength: tonesChunkLength,
	})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "sublime.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	syncEngine := &syncengine.FakeSyncEngine{}
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    whisperProvider,
		SyncEngine:  syncEngine,
		Stripper:    strip.NewFFStripper(),
		WorkerCount: 1,
	}
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	// Chunk boundaries sit at the generated silences.
	chunks := server.Chunks()
	if len(chunks) != len(chunkTranscripts) {
		t.Fatalf("whisper server got %d chunks, want %d", len(chunks), len(chunkTranscripts))
	}
	starts := []time.Duration{0}
	for i, wav := range chunks {
		samples := pcmSamples(t, wav)
		length := time.Duration(len(samples)) * time.Second / audiosource.SampleRate

		wantEnd := tonesDuration
		if i < len(tonesCuts) {
			wantEnd = tonesCuts[i]
		}
		if end := starts[i] + length; !withinTolerance(end, wantEnd) {
			t.Errorf("chunk %d ends at %v, want the silence at %v (±%v)", i+1, end, wantEnd, cutTolerance)
		}
		starts = append(starts, starts[i]+length)

		edge := int(edgeWindow * audiosource.SampleRate / time.Second)
		if i > 0 {
			if got := peak(samples[:edge]); got > silentPeak {
				t.Errorf("chunk %d starts in sound (peak %d), want silence", i+1, got)
			}
		}
		if i < len(tonesCuts) {
			if got := peak(samples[len(samples)-edge:]); got > silentPeak {
				t.Errorf("chunk %d ends in sound (peak %d), want silence", i+1, got)
			}
		}
		if got := peak(samples[len(samples)/2-edge : len(samples)/2+edge]); got < tonePeak {
			t.Errorf("chunk %d has no tone in its middle (peak %d)", i+1, got)
		}
	}

	// The final SRT carries each chunk's speech at the chunk's exact start
	// plus the chunk-relative time.
	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.BluRay.x264-TESTGROUP.en.srt")
	sidecar, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}
	cues := parseGeneratedCues(t, sidecar)
	if len(cues) != len(chunkTranscripts) {
		t.Fatalf("sidecar has %d cues, want %d:\n%s", len(cues), len(chunkTranscripts), sidecar)
	}
	words := []string{"one", "two", "three", "four"}
	nominalStarts := append([]time.Duration{0}, tonesCuts...)
	for i, cue := range cues {
		wantText := fmt.Sprintf("Chunk %s speaks.", words[i])
		if cue.text != wantText {
			t.Errorf("cue %d text = %q, want %q", i+1, cue.text, wantText)
		}
		// Offset by the chunk's measured start, not just the nominal cut.
		if want := starts[i] + chunkSpeechStart; cue.start < want-time.Millisecond || cue.start > want+time.Millisecond {
			t.Errorf("cue %d starts at %v, want its chunk's start %v + %v = %v", i+1, cue.start, starts[i], chunkSpeechStart, want)
		}
		if want := nominalStarts[i] + chunkSpeechStart; !withinTolerance(cue.start, want) {
			t.Errorf("cue %d starts at %v, want %v (±%v) from the generated silence", i+1, cue.start, want, cutTolerance)
		}
	}

	// A Generated Subtitle carries the Marker and is never Synced.
	codec, _ := marker.CodecFor(".srt")
	foundMarker, presence := codec.Read(sidecar)
	if presence != marker.Present {
		t.Fatalf("marker presence = %v, want Present; content:\n%s", presence, sidecar)
	}
	videoHash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		t.Fatalf("computing video hash: %v", err)
	}
	if foundMarker.ContentHash != videoHash {
		t.Errorf("marker hash = %q, want %q", foundMarker.ContentHash, videoHash)
	}
	if len(syncEngine.Calls) != 0 {
		t.Errorf("Sync was invoked %d times, want 0 for a Generated Subtitle", len(syncEngine.Calls))
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil || !found {
		t.Fatalf("GetFile found=%v err=%v", found, err)
	}
	if got := file.Languages[0].Status; got != domain.StatusSynced {
		t.Errorf("status = %q, want %q", got, domain.StatusSynced)
	}
}

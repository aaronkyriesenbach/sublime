package whisper_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/whisper"
)

// inferenceRequest is what the fake whisper server saw on one POST
// /inference.
type inferenceRequest struct {
	Fields map[string]string
	Audio  []byte
}

// fakeWhisperServer stands in for a whisper.cpp server: it records every
// /inference request and answers with a canned status and body.
type fakeWhisperServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []inferenceRequest
	status   int
	body     []byte

	// detectBody, if set, answers requests that ask for language detection
	// instead of a transcription.
	detectBody []byte

	// chunkBodies, if set, answers the n-th request with chunkBodies[n]
	// instead of body, so each chunk of one Download gets its own transcript.
	chunkBodies [][]byte

	// chunkStatuses, if set, answers the n-th request with chunkStatuses[n]
	// instead of status; a zero entry keeps status.
	chunkStatuses []int

	// hang makes the server hold every request open until the client goes
	// away, and signals on hung when one arrives.
	hang bool
	hung chan struct{}
	gone chan struct{}
}

func newFakeWhisperServer(t *testing.T, body []byte) *fakeWhisperServer {
	t.Helper()
	f := &fakeWhisperServer{
		status: http.StatusOK, body: body,
		hung: make(chan struct{}, 1), gone: make(chan struct{}, 1),
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := inferenceRequest{Fields: map[string]string{}}
		for k, v := range r.MultipartForm.Value {
			req.Fields[k] = v[0]
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		req.Audio, _ = io.ReadAll(file)

		f.mu.Lock()
		f.requests = append(f.requests, req)
		status, respBody := f.status, f.body
		if req.Fields["detect_language"] == "true" && f.detectBody != nil {
			respBody = f.detectBody
		}
		if n := len(f.requests) - 1; n < len(f.chunkBodies) {
			respBody = f.chunkBodies[n]
		}
		if n := len(f.requests) - 1; n < len(f.chunkStatuses) && f.chunkStatuses[n] != 0 {
			status = f.chunkStatuses[n]
		}
		hang := f.hang
		f.mu.Unlock()

		if hang {
			f.hung <- struct{}{}
			<-r.Context().Done()
			f.gone <- struct{}{}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWhisperServer) Requests() []inferenceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]inferenceRequest(nil), f.requests...)
}

func (f *fakeWhisperServer) DetectionRequests() []inferenceRequest {
	var detections []inferenceRequest
	for _, req := range f.Requests() {
		if req.Fields["detect_language"] == "true" {
			detections = append(detections, req)
		}
	}
	return detections
}

func sampleResponse(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/verbose_json_response.json")
	if err != nil {
		t.Fatalf("reading verbose JSON fixture: %v", err)
	}
	return body
}

const testChunkLength = 10 * time.Minute

func newProvider(t *testing.T, server *fakeWhisperServer, audio audiosource.Source, logOut io.Writer) *whisper.Provider {
	t.Helper()
	cfg := whisper.Config{Endpoint: server.URL, Audio: audio, ChunkLength: testChunkLength, Clock: &recordingClock{}}
	if logOut != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logOut, nil))
	}
	p, err := whisper.New(cfg)
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}
	return p
}

func query(tag language.Tag) provider.Query {
	return provider.Query{Title: "Test Movie", Year: 2024, Language: tag, Path: "/media/Test.Movie.2024.mkv"}
}

func TestNew_RequiresEndpointAndAudioSource(t *testing.T) {
	if _, err := whisper.New(whisper.Config{Audio: &audiosource.FakeSource{}, ChunkLength: testChunkLength}); err == nil {
		t.Error("New without Endpoint: want error, got nil")
	}
	if _, err := whisper.New(whisper.Config{Endpoint: "http://whisper:8080", ChunkLength: testChunkLength}); err == nil {
		t.Error("New without Audio: want error, got nil")
	}
}

func TestNew_RejectsNonPositiveChunkLength(t *testing.T) {
	for _, length := range []time.Duration{0, -time.Minute} {
		cfg := whisper.Config{Endpoint: "http://whisper:8080", Audio: &audiosource.FakeSource{}, ChunkLength: length}
		if _, err := whisper.New(cfg); err == nil {
			t.Errorf("New with ChunkLength %s: want error, got nil", length)
		}
	}
}

func TestSearch_MatchingAudioLanguageYieldsOneHashMatchCandidate(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}
	p := newProvider(t, newFakeWhisperServer(t, nil), audio, nil)

	got, err := p.Search(context.Background(), query(language.English))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d candidates, want 1: %+v", len(got), got)
	}
	if !got[0].HashMatch {
		t.Error("candidate HashMatch = false, want true so scoring always selects it")
	}
	if got[0].ID == "" {
		t.Error("candidate ID is empty")
	}
}

func TestSearch_RegionVariantTargetMatchesOnBaseLanguage(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "por"}}}
	p := newProvider(t, newFakeWhisperServer(t, nil), audio, nil)

	got, err := p.Search(context.Background(), query(language.BrazilianPortuguese))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d candidates, want 1", len(got))
	}
}

func TestSearch_LanguageMismatchIsAMissWithLoggedCause(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "jpn"}}}
	var logs bytes.Buffer
	p := newProvider(t, newFakeWhisperServer(t, nil), audio, &logs)

	got, err := p.Search(context.Background(), query(language.English))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search returned %d candidates, want 0", len(got))
	}
	if !strings.Contains(logs.String(), "audio is jpn, target is en") {
		t.Errorf("log does not state the cause %q:\n%s", "audio is jpn, target is en", logs.String())
	}
}

func TestSearch_NoMatchingStreamCausesAreLogged(t *testing.T) {
	tests := []struct {
		name    string
		streams []audiosource.Stream
		want    string
	}{
		{name: "no audio at all", streams: nil, want: "video has no audio streams"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			p := newProvider(t, newFakeWhisperServer(t, nil), &audiosource.FakeSource{Streams: tc.streams}, &logs)

			got, err := p.Search(context.Background(), query(language.English))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("Search returned %d candidates, want 0", len(got))
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Errorf("log does not contain %q:\n%s", tc.want, logs.String())
			}
		})
	}
}

func TestSearch_AudioProbeFailureIsAnError(t *testing.T) {
	probeErr := errors.New("ffprobe exploded")
	p := newProvider(t, newFakeWhisperServer(t, nil), &audiosource.FakeSource{StreamsErr: probeErr}, nil)

	if _, err := p.Search(context.Background(), query(language.English)); !errors.Is(err, probeErr) {
		t.Fatalf("Search error = %v, want it to wrap %v", err, probeErr)
	}
}

func TestDownload_TranscribesTheStreamTaggedWithTheTargetLanguage(t *testing.T) {
	audio := &audiosource.FakeSource{
		Streams: []audiosource.Stream{{Index: 1, Language: "jpn"}, {Index: 2, Language: "eng"}},
		Audio:   []byte("RIFF-fake-wav"),
	}
	server := newFakeWhisperServer(t, sampleResponse(t))
	p := newProvider(t, server, audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	if _, err := p.Download(context.Background(), candidates[0]); err != nil {
		t.Fatalf("Download: %v", err)
	}

	if len(audio.ExtractCalls) != 1 {
		t.Fatalf("Extract called %d times, want 1 (one request, no chunking yet)", len(audio.ExtractCalls))
	}
	call := audio.ExtractCalls[0]
	if call.VideoPath != "/media/Test.Movie.2024.mkv" || call.StreamIndex != 2 || call.Range != (audiosource.Range{}) {
		t.Errorf("Extract call = %+v, want whole stream 2 of the queried video", call)
	}

	requests := server.Requests()
	if len(requests) != 1 {
		t.Fatalf("sidecar saw %d requests, want 1", len(requests))
	}
	req := requests[0]
	if string(req.Audio) != "RIFF-fake-wav" {
		t.Errorf("sidecar audio = %q, want the extracted audio", req.Audio)
	}
	if req.Fields["language"] != "en" {
		t.Errorf("language = %q, want explicit %q", req.Fields["language"], "en")
	}
	if req.Fields["response_format"] != "verbose_json" {
		t.Errorf("response_format = %q, want verbose_json", req.Fields["response_format"])
	}
	if translate, sent := req.Fields["translate"]; sent && translate != "false" {
		t.Errorf("translate = %q, want it off", translate)
	}
}

func TestDownload_SendsBaseLanguageForRegionVariantTarget(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "por"}}}
	server := newFakeWhisperServer(t, sampleResponse(t))
	p := newProvider(t, server, audio, nil)

	candidates, err := p.Search(context.Background(), query(language.BrazilianPortuguese))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	if _, err := p.Download(context.Background(), candidates[0]); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := server.Requests()[0].Fields["language"]; got != "pt" {
		t.Errorf("language = %q, want %q", got, "pt")
	}
}

func TestDownload_ReturnsWordsMergedFromTokensAsSRT(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}
	p := newProvider(t, newFakeWhisperServer(t, sampleResponse(t)), audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	got, err := p.Download(context.Background(), candidates[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	want := "1\n00:00:00,500 --> 00:00:02,250\nHello there.\n\n" +
		"2\n00:00:03,000 --> 00:00:04,400\nGeneral Kenobi!\n"
	if string(got) != want {
		t.Errorf("SRT =\n%q\nwant\n%q", got, want)
	}
}

func TestDownload_SidecarErrorIsAnError(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}
	server := newFakeWhisperServer(t, []byte(`{"error":"failed to process audio"}`))
	server.status = http.StatusInternalServerError
	p := newProvider(t, server, audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	if _, err := p.Download(context.Background(), candidates[0]); err == nil {
		t.Fatal("Download: want an error for a 500 from the sidecar, got nil")
	}
}

func TestDownload_TranscriptWithNoSpeechIsAMiss(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}
	p := newProvider(t, newFakeWhisperServer(t, []byte(`{"segments":[]}`)), audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	_, err = p.Download(context.Background(), candidates[0])
	assertMiss(t, err)
}

func TestDownload_RejectsForeignCandidateID(t *testing.T) {
	p := newProvider(t, newFakeWhisperServer(t, nil), &audiosource.FakeSource{}, nil)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "/subtitle/123.zip"}); err == nil {
		t.Fatal("Download: want an error for a candidate this Provider did not produce, got nil")
	}
}

func TestNeverSynced(t *testing.T) {
	p := newProvider(t, newFakeWhisperServer(t, nil), &audiosource.FakeSource{}, nil)

	if !p.NeverSynced() {
		t.Error("NeverSynced() = false, want true: a Generated Subtitle is never Synced")
	}
}

// detectingServer answers language-detection requests with a verbose-JSON
// body whose language is code.
func detectingServer(t *testing.T, code string) *fakeWhisperServer {
	t.Helper()
	server := newFakeWhisperServer(t, sampleResponse(t))
	server.detectBody = []byte(`{"language":"` + code + `"}`)
	return server
}

func TestSearch_UntaggedAudioDetectedAsTargetIsEligible(t *testing.T) {
	// audiosource reports an explicit "und" tag as untagged too, so both
	// shapes arrive as an empty Language.
	audio := &audiosource.FakeSource{
		Streams:       []audiosource.Stream{{Index: 3}},
		VideoDuration: 2 * time.Hour,
	}
	server := detectingServer(t, "en")
	p := newProvider(t, server, audio, nil)

	got, err := p.Search(context.Background(), query(language.English))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || !got[0].HashMatch {
		t.Fatalf("Search = %+v, want one hash-match candidate", got)
	}
	if n := len(server.DetectionRequests()); n != 1 {
		t.Errorf("sidecar saw %d detection requests, want 1", n)
	}
}

func TestSearch_DetectionUsesAClipFromTheMiddleOfTheFile(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     audiosource.Range
	}{
		{
			name:     "long file: 30s centred on the midpoint",
			duration: 2 * time.Hour,
			want:     audiosource.Range{Start: time.Hour - 15*time.Second, Duration: 30 * time.Second},
		},
		{
			name:     "file shorter than the clip: the whole file",
			duration: 20 * time.Second,
			want:     audiosource.Range{Start: 0, Duration: 20 * time.Second},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			audio := &audiosource.FakeSource{
				Streams:       []audiosource.Stream{{Index: 1, Language: "jpn"}, {Index: 2}},
				VideoDuration: tc.duration,
			}
			p := newProvider(t, detectingServer(t, "en"), audio, nil)

			if _, err := p.Search(context.Background(), query(language.English)); err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(audio.ExtractCalls) != 1 {
				t.Fatalf("Extract called %d times, want 1: %+v", len(audio.ExtractCalls), audio.ExtractCalls)
			}
			call := audio.ExtractCalls[0]
			if call.VideoPath != "/media/Test.Movie.2024.mkv" || call.StreamIndex != 2 || call.Range != tc.want {
				t.Errorf("Extract call = %+v, want untagged stream 2 with range %+v", call, tc.want)
			}
		})
	}
}

func TestSearch_DetectionAsksSidecarToDetectNotTranscribeOrTranslate(t *testing.T) {
	audio := &audiosource.FakeSource{
		Streams:       []audiosource.Stream{{Index: 1}},
		VideoDuration: time.Hour,
		Audio:         []byte("RIFF-clip"),
	}
	server := detectingServer(t, "en")
	p := newProvider(t, server, audio, nil)

	if _, err := p.Search(context.Background(), query(language.English)); err != nil {
		t.Fatalf("Search: %v", err)
	}
	reqs := server.DetectionRequests()
	if len(reqs) != 1 {
		t.Fatalf("sidecar saw %d detection requests, want 1", len(reqs))
	}
	if string(reqs[0].Audio) != "RIFF-clip" {
		t.Errorf("sidecar audio = %q, want the extracted clip", reqs[0].Audio)
	}
	if reqs[0].Fields["language"] != "auto" {
		t.Errorf("language = %q, want %q so the sidecar detects", reqs[0].Fields["language"], "auto")
	}
	if translate, sent := reqs[0].Fields["translate"]; sent && translate != "false" {
		t.Errorf("translate = %q, want it off", translate)
	}
}

func TestSearch_DetectedLanguageDifferingFromTargetIsAMissWithLoggedCause(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, VideoDuration: time.Hour}
	var logs bytes.Buffer
	p := newProvider(t, detectingServer(t, "ja"), audio, &logs)

	got, err := p.Search(context.Background(), query(language.English))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search returned %d candidates, want 0: a wrong-language guess must never become a transcript", len(got))
	}
	if !strings.Contains(logs.String(), "detected language is ja, target is en") {
		t.Errorf("log does not state the cause:\n%s", logs.String())
	}
}

func TestSearch_DetectionComparesOnBaseLanguage(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, VideoDuration: time.Hour}
	p := newProvider(t, detectingServer(t, "pt"), audio, nil)

	got, err := p.Search(context.Background(), query(language.BrazilianPortuguese))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d candidates, want 1 for detected pt vs pt-BR target", len(got))
	}
}

func TestSearch_DetectionResponseShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "language code", body: `{"language":"en"}`, want: true},
		{name: "detected_language code", body: `{"detected_language":"en","detected_language_probability":0.97}`, want: true},
		{name: "language full name", body: `{"language":"english"}`, want: true},
		{name: "detected_language full name", body: `{"detected_language":"English"}`, want: true},
		{name: "other full name", body: `{"detected_language":"japanese","language":"ja"}`, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, VideoDuration: time.Hour}
			server := newFakeWhisperServer(t, nil)
			server.detectBody = []byte(tc.body)
			p := newProvider(t, server, audio, nil)

			got, err := p.Search(context.Background(), query(language.English))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if (len(got) == 1) != tc.want {
				t.Errorf("Search returned %d candidates, want eligible = %v", len(got), tc.want)
			}
		})
	}
}

func TestSearch_UnintelligibleDetectionResponseIsAnError(t *testing.T) {
	for _, body := range []string{`{}`, `{"language":"  "}`, `not json`} {
		audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, VideoDuration: time.Hour}
		server := newFakeWhisperServer(t, nil)
		server.detectBody = []byte(body)
		p := newProvider(t, server, audio, nil)

		if _, err := p.Search(context.Background(), query(language.English)); err == nil {
			t.Errorf("Search with detection body %q: want an error, got nil", body)
		}
	}
}

func TestSearch_DetectionFailuresAreErrors(t *testing.T) {
	t.Run("sidecar error", func(t *testing.T) {
		audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, VideoDuration: time.Hour}
		server := detectingServer(t, "en")
		server.status = http.StatusInternalServerError
		p := newProvider(t, server, audio, nil)

		if _, err := p.Search(context.Background(), query(language.English)); err == nil {
			t.Fatal("Search: want an error for a 500 from the sidecar, got nil")
		}
	})
	t.Run("unknown duration", func(t *testing.T) {
		durationErr := errors.New("ffprobe exploded")
		audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, DurationErr: durationErr}
		p := newProvider(t, detectingServer(t, "en"), audio, nil)

		if _, err := p.Search(context.Background(), query(language.English)); !errors.Is(err, durationErr) {
			t.Fatalf("Search error = %v, want it to wrap %v", err, durationErr)
		}
	})
	t.Run("clip extraction", func(t *testing.T) {
		extractErr := errors.New("ffmpeg exploded")
		audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, VideoDuration: time.Hour, ExtractErr: extractErr}
		p := newProvider(t, detectingServer(t, "en"), audio, nil)

		if _, err := p.Search(context.Background(), query(language.English)); !errors.Is(err, extractErr) {
			t.Fatalf("Search error = %v, want it to wrap %v", err, extractErr)
		}
	})
}

func TestSearch_NeverDetectsWhenATaggedStreamMatches(t *testing.T) {
	audio := &audiosource.FakeSource{
		Streams:       []audiosource.Stream{{Index: 1}, {Index: 2, Language: "eng"}},
		VideoDuration: time.Hour,
	}
	server := detectingServer(t, "ja")
	p := newProvider(t, server, audio, nil)

	got, err := p.Search(context.Background(), query(language.English))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d candidates, want 1 from the tagged stream", len(got))
	}
	if n := len(server.Requests()); n != 0 {
		t.Errorf("sidecar saw %d requests, want 0: a matching tag needs no detection", n)
	}
	if len(audio.ExtractCalls) != 0 {
		t.Errorf("Extract called %d times during Search, want 0", len(audio.ExtractCalls))
	}
}

func TestSearch_NeverDetectsWhenOnlyMismatchingTagsExist(t *testing.T) {
	audio := &audiosource.FakeSource{
		Streams:       []audiosource.Stream{{Index: 1, Language: "jpn"}, {Index: 2, Language: "fra"}},
		VideoDuration: time.Hour,
	}
	server := detectingServer(t, "en")
	p := newProvider(t, server, audio, nil)

	got, err := p.Search(context.Background(), query(language.English))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search returned %d candidates, want 0", len(got))
	}
	if n := len(server.Requests()); n != 0 {
		t.Errorf("sidecar saw %d requests, want 0: mismatching tags are not uncertainty", n)
	}
}

func TestDownload_MultiTrackPicksTheFirstTrackTaggedWithTheTarget(t *testing.T) {
	audio := &audiosource.FakeSource{
		Streams: []audiosource.Stream{
			{Index: 1, Language: "jpn"},
			{Index: 2, Language: "eng"},
			{Index: 3, Language: "eng"},
		},
	}
	p := newProvider(t, newFakeWhisperServer(t, sampleResponse(t)), audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	if _, err := p.Download(context.Background(), candidates[0]); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := audio.ExtractCalls[len(audio.ExtractCalls)-1].StreamIndex; got != 2 {
		t.Errorf("transcribed stream = %d, want 2 (the first English track)", got)
	}
}

func TestDownload_TranscribesTheUntaggedStreamAcceptedByDetection(t *testing.T) {
	audio := &audiosource.FakeSource{
		Streams:       []audiosource.Stream{{Index: 1, Language: "jpn"}, {Index: 2}},
		VideoDuration: 11 * time.Minute,
	}
	server := detectingServer(t, "en")
	p := newProvider(t, server, audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	if _, err := p.Download(context.Background(), candidates[0]); err != nil {
		t.Fatalf("Download: %v", err)
	}

	last := audio.ExtractCalls[len(audio.ExtractCalls)-1]
	if last.StreamIndex != 2 {
		t.Errorf("Download extracted %+v, want the untagged stream 2", last)
	}
	var transcription *inferenceRequest
	for _, req := range server.Requests() {
		if req.Fields["detect_language"] != "true" {
			transcription = &req
		}
	}
	if transcription == nil || transcription.Fields["language"] != "en" {
		t.Errorf("transcription request = %+v, want explicit language en", transcription)
	}
}

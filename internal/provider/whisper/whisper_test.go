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
}

func newFakeWhisperServer(t *testing.T, body []byte) *fakeWhisperServer {
	t.Helper()
	f := &fakeWhisperServer{status: http.StatusOK, body: body}
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
		f.mu.Unlock()

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

func sampleResponse(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/verbose_json_response.json")
	if err != nil {
		t.Fatalf("reading verbose JSON fixture: %v", err)
	}
	return body
}

func newProvider(t *testing.T, server *fakeWhisperServer, audio audiosource.Source, logOut io.Writer) *whisper.Provider {
	t.Helper()
	cfg := whisper.Config{Endpoint: server.URL, Audio: audio, Clock: &recordingClock{}}
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
	if _, err := whisper.New(whisper.Config{Audio: &audiosource.FakeSource{}}); err == nil {
		t.Error("New without Endpoint: want error, got nil")
	}
	if _, err := whisper.New(whisper.Config{Endpoint: "http://whisper:8080"}); err == nil {
		t.Error("New without Audio: want error, got nil")
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
		{name: "untagged audio", streams: []audiosource.Stream{{Index: 1}}, want: "audio is untagged, target is en"},
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

func TestDownload_ReturnsSegmentsAsSRT(t *testing.T) {
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
		"2\n00:00:03,000 --> 01:01:01,789\nGeneral Kenobi!\n"
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

func TestDownload_TranscriptWithNoSpeechIsAnError(t *testing.T) {
	audio := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}
	p := newProvider(t, newFakeWhisperServer(t, []byte(`{"segments":[]}`)), audio, nil)

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	if _, err := p.Download(context.Background(), candidates[0]); err == nil {
		t.Fatal("Download: want an error rather than an empty subtitle, got nil")
	}
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

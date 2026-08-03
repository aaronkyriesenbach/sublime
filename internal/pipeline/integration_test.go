package pipeline_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider/opensubtitles"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine/alass"
)

const (
	integrationVideoDir = "../../testdata/integration/video"
	integrationSubsDir  = "../../testdata/integration/subs"
)

func requireRealBinaries(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"alass", "ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary not found on PATH; skipping real-pipeline integration test", bin)
		}
	}
}

// TestIntegration_RealPipeline_EndToEnd runs the full pipeline with:
// - Real ffmpeg/ffprobe for stripping/probing
// - Real alass for subtitle synchronization
// - httptest-backed mock OpenSubtitles server for Provider
//
// This validates that the production wiring produces a correctly
// Marker-embedded synced sidecar.
func TestIntegration_RealPipeline_EndToEnd(t *testing.T) {
	requireRealBinaries(t)

	libDir := t.TempDir()

	videoName := "Test.Movie.2024.BluRay.x264-TESTGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join(integrationVideoDir, "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	subtitleContent, err := os.ReadFile(filepath.Join(integrationSubsDir, "sample.shifted.srt"))
	if err != nil {
		t.Fatalf("reading shifted subtitle: %v", err)
	}

	movieHash, err := opensubtitles.ComputeMovieHash(videoPath)
	if err != nil {
		t.Fatalf("computing moviehash: %v", err)
	}

	mock := newIntegrationMockServer(t, movieHash, subtitleContent)

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	provider, err := opensubtitles.New(opensubtitles.Config{
		Secrets: config.OpenSubtitlesSecrets{
			APIKey:   "test-api-key",
			Username: "testuser",
			Password: "testpass",
		},
		BaseURL: mock.URL(),
	})
	if err != nil {
		t.Fatalf("creating provider: %v", err)
	}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    provider,
		SyncEngine:  alass.New(),
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
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}

	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
	if result.Synced != 1 {
		t.Errorf("Synced = %d, want 1", result.Synced)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0; errors: %v", result.Failed, result.Errors)
	}

	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.BluRay.x264-TESTGROUP.en.srt")
	sidecarContent, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}

	codec, _ := marker.CodecFor(".srt")
	foundMarker, presence := codec.Read(sidecarContent)
	if presence != marker.Present {
		t.Errorf("marker presence = %v, want Present; content:\n%s", presence, sidecarContent)
	}

	videoHash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		t.Fatalf("computing video hash: %v", err)
	}
	if foundMarker.ContentHash != videoHash {
		t.Errorf("marker hash = %q, want %q", foundMarker.ContentHash, videoHash)
	}

	originalShifted, _ := os.ReadFile(filepath.Join(integrationSubsDir, "sample.shifted.srt"))
	if string(sidecarContent) == string(originalShifted) {
		t.Error("expected sidecar to differ from the shifted input (alass re-timing + marker embedding)")
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from store: %v", err)
	}
	if !found {
		t.Fatal("file not found in store")
	}
	if len(file.Languages) != 1 {
		t.Fatalf("file.Languages = %d, want 1", len(file.Languages))
	}
	if file.Languages[0].Status != domain.StatusSynced {
		t.Errorf("file status = %q, want %q", file.Languages[0].Status, domain.StatusSynced)
	}

	result2, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("second pipeline.Run: %v", err)
	}
	if result2.Skipped != 1 {
		t.Errorf("second run Skipped = %d, want 1 (marker recognized)", result2.Skipped)
	}
	if result2.Synced != 0 {
		t.Errorf("second run Synced = %d, want 0", result2.Synced)
	}
}

// TestIntegration_NewProduction validates that NewProduction constructs
// a working pipeline with real implementations.
func TestIntegration_NewProduction_Success(t *testing.T) {
	requireRealBinaries(t)

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	p, err := pipeline.NewProduction(pipeline.ProductionConfig{
		Store: st,
		Secrets: config.OpenSubtitlesSecrets{
			APIKey:   "test-api-key",
			Username: "testuser",
			Password: "testpass",
		},
		WorkerCount: 1,
	})
	if err != nil {
		t.Fatalf("NewProduction() error = %v", err)
	}

	if p.Store != st {
		t.Error("pipeline.Store not set correctly")
	}
	if p.Provider == nil {
		t.Error("pipeline.Provider is nil")
	}
	if p.SyncEngine == nil {
		t.Error("pipeline.SyncEngine is nil")
	}
	if p.Stripper == nil {
		t.Error("pipeline.Stripper is nil")
	}
}

func TestIntegration_NewProduction_MissingSecrets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	_, err = pipeline.NewProduction(pipeline.ProductionConfig{
		Store:   st,
		Secrets: config.OpenSubtitlesSecrets{},
	})
	if err == nil {
		t.Fatal("expected an error when secrets are missing")
	}
}

// integrationMockServer is a minimal mock OpenSubtitles server for
// integration tests. It returns canned responses for the search, login,
// and download endpoints.
type integrationMockServer struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []integrationRequest

	movieHash       string
	subtitleContent []byte
}

type integrationRequest struct {
	Method string
	Path   string
}

func newIntegrationMockServer(t *testing.T, movieHash string, subtitleContent []byte) *integrationMockServer {
	t.Helper()
	m := &integrationMockServer{
		t:               t,
		movieHash:       movieHash,
		subtitleContent: subtitleContent,
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *integrationMockServer) URL() string { return m.server.URL }

func (m *integrationMockServer) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.requests = append(m.requests, integrationRequest{Method: r.Method, Path: r.URL.Path})
	m.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/subtitles":
		m.handleSearch(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/login":
		m.handleLogin(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/download":
		m.handleDownload(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/signed/subtitle.srt":
		m.handleSignedURL(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *integrationMockServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"data": []map[string]interface{}{
			{
				"attributes": map[string]interface{}{
					"release": "Test.Movie.2024.BluRay.x264-TESTGROUP",
					"feature_details": map[string]interface{}{
						"title":          "Test Movie",
						"year":           2024,
						"season_number":  0,
						"episode_number": 0,
					},
					"files": []map[string]interface{}{
						{"file_id": 12345, "cd_number": 1, "file_name": "subtitle.srt"},
					},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Errorf("encoding search response: %v", err)
	}
}

func (m *integrationMockServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"token": fmt.Sprintf("jwt-token-%d", time.Now().UnixNano()),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Errorf("encoding login response: %v", err)
	}
}

func (m *integrationMockServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"link": m.URL() + "/signed/subtitle.srt",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Errorf("encoding download response: %v", err)
	}
}

func (m *integrationMockServer) handleSignedURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	if _, err := w.Write(m.subtitleContent); err != nil {
		m.t.Errorf("writing subtitle content: %v", err)
	}
}

package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

func TestPipeline_EndToEnd(t *testing.T) {
	// Create a temp directory as the "library"
	libDir := t.TempDir()

	// Copy the sample video with a parseable filename
	// Format: "Title 2024 HDTV x264-GROUP.mp4" for scoring.Parse to extract
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	// Create a foreign sidecar (no Marker) that should be stripped
	foreignSidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	foreignContent := "1\n00:00:01,000 --> 00:00:02,000\nForeign subtitle\n"
	if err := os.WriteFile(foreignSidecarPath, []byte(foreignContent), 0o644); err != nil {
		t.Fatalf("writing foreign sidecar: %v", err)
	}

	// Set up the store
	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Set up fakes
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n\n2\n00:00:03,500 --> 00:00:05,100\nFour five six\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{
				ID:           "test-candidate-1",
				Title:        "Test Movie",
				Year:         2024,
				Source:       "HDTV",
				ReleaseGroup: "FAKEGROUP",
				HashMatch:    false,
			}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	fakeSyncEngine := &syncengine.FakeSyncEngine{}
	fakeStripper := &pipeline.FakeStripper{}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  fakeSyncEngine,
		Stripper:    fakeStripper,
		WorkerCount: 1, // Deterministic single-threaded for testing
	}

	// First run: should sync the file
	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
	if result.Synced != 1 {
		t.Errorf("Synced = %d, want 1", result.Synced)
	}
	if result.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", result.Skipped)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0; errors: %v", result.Failed, result.Errors)
	}

	// Verify the sync engine was called
	if len(fakeSyncEngine.Calls) != 1 {
		t.Errorf("SyncEngine.Calls = %d, want 1", len(fakeSyncEngine.Calls))
	}

	// Verify the stripper was called
	if len(fakeStripper.Calls) != 1 {
		t.Errorf("Stripper.Calls = %d, want 1", len(fakeStripper.Calls))
	}

	// Verify a Marker-embedded sidecar was written
	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	sidecarContent, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}

	codec, _ := marker.CodecFor(".srt")
	foundMarker, presence := codec.Read(sidecarContent)
	if presence != marker.Present {
		t.Errorf("marker presence = %v, want Present; content:\n%s", presence, sidecarContent)
	}

	// Verify the marker matches the video's content hash
	videoHash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		t.Fatalf("computing video hash: %v", err)
	}
	if foundMarker.ContentHash != videoHash {
		t.Errorf("marker hash = %q, want %q", foundMarker.ContentHash, videoHash)
	}

	// Verify the state store shows "synced"
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

	// Second run: should skip (Marker recognized)
	fakeStripper.Calls = nil
	fakeSyncEngine.Calls = nil

	result2, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if result2.FilesScanned != 1 {
		t.Errorf("second run FilesScanned = %d, want 1", result2.FilesScanned)
	}
	if result2.Synced != 0 {
		t.Errorf("second run Synced = %d, want 0", result2.Synced)
	}
	if result2.Skipped != 1 {
		t.Errorf("second run Skipped = %d, want 1", result2.Skipped)
	}
	if result2.Failed != 0 {
		t.Errorf("second run Failed = %d, want 0", result2.Failed)
	}

	// Verify no sync engine or stripper calls on re-run
	if len(fakeSyncEngine.Calls) != 0 {
		t.Errorf("second run SyncEngine.Calls = %d, want 0", len(fakeSyncEngine.Calls))
	}
	if len(fakeStripper.Calls) != 0 {
		t.Errorf("second run Stripper.Calls = %d, want 0", len(fakeStripper.Calls))
	}
}

func TestPipeline_NoCandidate(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Provider returns candidates that don't match (wrong title/year)
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{
				ID:    "wrong-candidate",
				Title: "Wrong Title",
				Year:  1999,
			}}, nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
	}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Should be counted as NoCandidate, not Synced or Failed with an error
	if result.NoCandidate != 1 {
		t.Errorf("NoCandidate = %d, want 1", result.NoCandidate)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
	if result.Synced != 0 {
		t.Errorf("Synced = %d, want 0", result.Synced)
	}

	// Verify the store shows "failed" with no_candidate reason
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
	if file.Languages[0].Status != domain.StatusFailed {
		t.Errorf("file status = %q, want %q", file.Languages[0].Status, domain.StatusFailed)
	}
	if file.Languages[0].FailureReason != domain.FailureNoCandidate {
		t.Errorf("failure reason = %q, want %q", file.Languages[0].FailureReason, domain.FailureNoCandidate)
	}
}

func TestPipeline_MultipleLanguages(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{
				ID:        "test-candidate",
				Title:     "Test Movie",
				Year:      2024,
				HashMatch: false,
			}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English, language.Spanish},
		StripScope: domain.StripScopeAll,
	}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
	}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
	if result.Synced != 2 {
		t.Errorf("Synced = %d, want 2 (one per language)", result.Synced)
	}

	// Verify both sidecars exist
	enSidecar := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	esSidecar := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.es.srt")

	if _, err := os.Stat(enSidecar); os.IsNotExist(err) {
		t.Error("English sidecar not found")
	}
	if _, err := os.Stat(esSidecar); os.IsNotExist(err) {
		t.Error("Spanish sidecar not found")
	}
}

func TestPipeline_ScanFindsVideoFiles(t *testing.T) {
	libDir := t.TempDir()

	// Create subdirectory structure
	subDir := filepath.Join(libDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	// Copy video to both root and subdir
	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}

	video1 := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	video2 := filepath.Join(subDir, "Movie.Two.2021.HDTV.x264-GRP.mkv")

	if err := os.WriteFile(video1, videoContent, 0o644); err != nil {
		t.Fatalf("writing video1: %v", err)
	}
	if err := os.WriteFile(video2, videoContent, 0o644); err != nil {
		t.Fatalf("writing video2: %v", err)
	}

	// Also create a non-video file that should be ignored
	txtFile := filepath.Join(libDir, "readme.txt")
	if err := os.WriteFile(txtFile, []byte("not a video"), 0o644); err != nil {
		t.Fatalf("writing txt file: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	searchCount := 0
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			searchCount++
			title := "Movie One"
			year := 2020
			if strings.Contains(q.Path, "Two") {
				title = "Movie Two"
				year = 2021
			}
			return []domain.Candidate{{
				ID:    "candidate",
				Title: title,
				Year:  year,
			}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
	}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", result.FilesScanned)
	}
	if result.Synced != 2 {
		t.Errorf("Synced = %d, want 2", result.Synced)
	}
}

func TestPipeline_RunWithForceReprocessesSyncedFile(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "test-candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	fakeSyncEngine := &syncengine.FakeSyncEngine{}
	fakeStripper := &pipeline.FakeStripper{}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  fakeSyncEngine,
		Stripper:    fakeStripper,
		WorkerCount: 1,
	}

	ctx := context.Background()

	// First run: synced normally.
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run without Force: gate skips it, no Provider/SyncEngine/Stripper calls.
	fakeSyncEngine.Calls = nil
	fakeStripper.Calls = nil
	result2, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result2.Skipped != 1 {
		t.Errorf("second run Skipped = %d, want 1", result2.Skipped)
	}
	if len(fakeSyncEngine.Calls) != 0 {
		t.Errorf("second run SyncEngine.Calls = %d, want 0", len(fakeSyncEngine.Calls))
	}

	// Third run with WithForce: bypasses the gate and reprocesses despite the
	// valid Marker from the first run.
	result3, err := p.Run(ctx, lib, pipeline.WithForce())
	if err != nil {
		t.Fatalf("forced run: %v", err)
	}
	if result3.Synced != 1 {
		t.Errorf("forced run Synced = %d, want 1", result3.Synced)
	}
	if result3.Skipped != 0 {
		t.Errorf("forced run Skipped = %d, want 0", result3.Skipped)
	}
	if len(fakeSyncEngine.Calls) != 1 {
		t.Errorf("forced run SyncEngine.Calls = %d, want 1", len(fakeSyncEngine.Calls))
	}
	if len(fakeStripper.Calls) != 1 {
		t.Errorf("forced run Stripper.Calls = %d, want 1", len(fakeStripper.Calls))
	}
}

func TestPipeline_RunFileProcessesSingleFile(t *testing.T) {
	libDir := t.TempDir()

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}

	video1 := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	video2 := filepath.Join(libDir, "Movie.Two.2021.HDTV.x264-GRP.mp4")
	if err := os.WriteFile(video1, videoContent, 0o644); err != nil {
		t.Fatalf("writing video1: %v", err)
	}
	if err := os.WriteFile(video2, videoContent, 0o644); err != nil {
		t.Fatalf("writing video2: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	searchedPaths := map[string]int{}
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			searchedPaths[q.Path]++
			return []domain.Candidate{{ID: "candidate", Title: q.Title, Year: q.Year}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
	}

	ctx := context.Background()
	result, err := p.RunFile(ctx, lib, video1)
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}

	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
	if result.Synced != 1 {
		t.Errorf("Synced = %d, want 1", result.Synced)
	}
	if searchedPaths[video1] != 1 {
		t.Errorf("searches for video1 = %d, want 1", searchedPaths[video1])
	}
	if searchedPaths[video2] != 0 {
		t.Errorf("searches for video2 = %d, want 0 (RunFile must not touch other files)", searchedPaths[video2])
	}

	// video2's sidecar should not exist since RunFile only touched video1.
	video2Sidecar := filepath.Join(libDir, "Movie.Two.2021.HDTV.x264-GRP.en.srt")
	if _, err := os.Stat(video2Sidecar); !os.IsNotExist(err) {
		t.Errorf("video2 sidecar should not exist, stat err = %v", err)
	}
}

// TestPipeline_LogsFailureWithUnderlyingError guards the observability fix:
// a per-file failure must be logged with the real underlying error, not
// just silently recorded as a coarse status in the store.
func TestPipeline_LogsFailureWithUnderlyingError(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	wantErr := errors.New("provider unavailable: connection refused")
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return nil, wantErr
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", result.Failed)
	}

	logOutput := logBuf.String()
	for _, want := range []string{"library=test-library", "path=" + videoPath, "connection refused"} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}
}

// TestPipeline_LogsNoCandidateOutcome guards the same observability fix for
// the no-candidate terminal state, which records no error but should still
// surface in logs — not just as a queryable store status.
func TestPipeline_LogsNoCandidateOutcome(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "wrong-candidate", Title: "Wrong Title", Year: 1999}}, nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.NoCandidate != 1 {
		t.Fatalf("NoCandidate = %d, want 1", result.NoCandidate)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "no candidate") {
		t.Errorf("log output = %q, want it to mention no candidate cleared the cutoff", logOutput)
	}
	if !strings.Contains(logOutput, "path="+videoPath) {
		t.Errorf("log output = %q, want it to contain path=%s", logOutput, videoPath)
	}
}

// TestPipeline_ContentHashReflectsPostStripState guards the Content Hash /
// Strip-ordering fix: when Strip's embedded-stream removal actually
// mutates the video, the Marker and store must bind to the file's settled
// post-Strip hash, not the pre-Strip hash captured at the top of the
// per-file step. It uses FakeStripper (no real ffmpeg/ffprobe) configured
// to simulate a removed stream by mutating the video's bytes.
func TestPipeline_ContentHashReflectsPostStripState(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	if err := os.WriteFile(videoPath, []byte("original video bytes"), 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "test-candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	// Simulate a Strip pass that finds and removes an embedded stream,
	// mutating the video's on-disk bytes the way a real ffmpeg remux would.
	fakeStripper := &pipeline.FakeStripper{StripEmbeddedIndices: []int{2}}

	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    fakeStripper,
		WorkerCount: 1,
	}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if result.Synced != 1 {
		t.Fatalf("Synced = %d, want 1; errors: %v", result.Synced, result.Errors)
	}
	if len(fakeStripper.StripEmbeddedCalls) != 1 {
		t.Fatalf("StripEmbeddedCalls = %d, want 1", len(fakeStripper.StripEmbeddedCalls))
	}

	// The video's Content Hash after the (simulated) Strip mutation.
	postStripHash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		t.Fatalf("computing post-strip video hash: %v", err)
	}

	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	sidecarContent, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}
	codec, _ := marker.CodecFor(".srt")
	foundMarker, presence := codec.Read(sidecarContent)
	if presence != marker.Present {
		t.Fatalf("marker presence = %v, want Present", presence)
	}
	if foundMarker.ContentHash != postStripHash {
		t.Errorf("marker hash = %q, want post-strip hash %q", foundMarker.ContentHash, postStripHash)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from store: %v", err)
	}
	if !found {
		t.Fatal("file not found in store")
	}
	if file.ContentHash != string(postStripHash) {
		t.Errorf("store content hash = %q, want post-strip hash %q", file.ContentHash, postStripHash)
	}
	if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("file.Languages = %+v, want 1 entry with status synced", file.Languages)
	}

	// Simulate total state loss: a brand-new, empty state store.
	freshDBPath := filepath.Join(t.TempDir(), "fresh.db")
	freshStore, err := store.Open(freshDBPath)
	if err != nil {
		t.Fatalf("opening fresh store: %v", err)
	}
	defer func() { _ = freshStore.Close() }()

	fakeStripper.StripEmbeddedCalls = nil
	fakeSyncEngine := &syncengine.FakeSyncEngine{}
	p2 := &pipeline.Pipeline{
		Store:       freshStore,
		Provider:    fakeProvider,
		SyncEngine:  fakeSyncEngine,
		Stripper:    fakeStripper,
		WorkerCount: 1,
	}

	result2, err := p2.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run against fresh store: %v", err)
	}
	if result2.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (Marker recognized despite total state loss)", result2.Skipped)
	}
	if result2.Synced != 0 {
		t.Errorf("Synced = %d, want 0 (should not reprocess)", result2.Synced)
	}
	if len(fakeSyncEngine.Calls) != 0 {
		t.Errorf("SyncEngine.Calls = %d, want 0 (should not reprocess)", len(fakeSyncEngine.Calls))
	}
	if len(fakeStripper.StripEmbeddedCalls) != 0 {
		t.Errorf("StripEmbeddedCalls = %d, want 0 (should not reprocess)", len(fakeStripper.StripEmbeddedCalls))
	}
}

// TestPipeline_LogsFileFoundOncePerFileNotPerLanguage guards the
// Found/Changed classification's per-file (not per-language) logging
// contract: a brand-new file with multiple configured languages must
// produce exactly one "file found" line, not one per (file, language) pair.
func TestPipeline_LogsFileFoundOncePerFileNotPerLanguage(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English, language.Spanish},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 2,
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	logOutput := logBuf.String()
	if got := strings.Count(logOutput, "file found"); got != 1 {
		t.Errorf(`"file found" count = %d, want 1; log output:\n%s`, got, logOutput)
	}
	if strings.Contains(logOutput, "file changed") {
		t.Errorf("log output unexpectedly contains \"file changed\" for a brand-new file:\n%s", logOutput)
	}
}

// TestPipeline_ChangedFileLogsFileChangedAndResetsToPending guards the
// Changed half of the classification for the bulk-scan entrypoint: an
// already-tracked file whose Content Hash differs from its last-recorded
// value must log "file changed" (not another "file found") and reset its
// language state back to Pending in place.
func TestPipeline_ChangedFileLogsFileChangedAndResetsToPending(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// An external edit: mutate the video's bytes so its Content Hash
	// changes, without going through Sublime's own Strip pass.
	if err := os.WriteFile(videoPath, append(videoContent, []byte("mutated")...), 0o644); err != nil {
		t.Fatalf("mutating video: %v", err)
	}
	// Remove the sidecar written by the first run so the marker gate
	// doesn't short-circuit the second run before we can observe the
	// Pending reset play out into a fresh sync.
	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	if err := os.Remove(sidecarPath); err != nil {
		t.Fatalf("removing sidecar: %v", err)
	}

	logBuf.Reset()
	result2, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "file changed") {
		t.Errorf(`log output missing "file changed":%s`, logOutput)
	}
	if strings.Contains(logOutput, "file found") {
		t.Errorf(`log output unexpectedly contains "file found" for an already-tracked file:%s`, logOutput)
	}

	if result2.Synced != 1 {
		t.Errorf("second run Synced = %d, want 1 (reset to Pending then resynced); errors: %v", result2.Synced, result2.Errors)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from store: %v", err)
	}
	if !found {
		t.Fatal("file not found in store")
	}
	if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("file.Languages = %+v, want 1 entry with status synced", file.Languages)
	}
}

// TestPipeline_RunFileAppliesSameFoundVsChangedClassificationAsRun guards
// the single-file entrypoint (fsnotify watch events, manual reprocessing)
// applying the identical Found-vs-Changed distinction as the bulk scan.
func TestPipeline_RunFileAppliesSameFoundVsChangedClassificationAsRun(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()

	// A brand-new file, seen via the single-file entrypoint, logs "file
	// found".
	if _, err := p.RunFile(ctx, lib, videoPath); err != nil {
		t.Fatalf("first RunFile: %v", err)
	}
	if got := strings.Count(logBuf.String(), "file found"); got != 1 {
		t.Errorf(`"file found" count = %d, want 1; log output:%s`, got, logBuf.String())
	}

	// An external edit changes the Content Hash; the sidecar is removed so
	// the marker gate doesn't short-circuit before the resync.
	if err := os.WriteFile(videoPath, append(videoContent, []byte("mutated")...), 0o644); err != nil {
		t.Fatalf("mutating video: %v", err)
	}
	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	if err := os.Remove(sidecarPath); err != nil {
		t.Fatalf("removing sidecar: %v", err)
	}

	logBuf.Reset()
	if _, err := p.RunFile(ctx, lib, videoPath); err != nil {
		t.Fatalf("second RunFile: %v", err)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "file changed") {
		t.Errorf(`log output missing "file changed":%s`, logOutput)
	}
	if strings.Contains(logOutput, "file found") {
		t.Errorf(`log output unexpectedly contains "file found" for an already-tracked file:%s`, logOutput)
	}
}

// TestPipeline_ForceReprocessLogsFileChangedNotFileFound guards a manual
// reprocess request (pipeline.WithForce) against an already-tracked file
// whose Content Hash hasn't changed: it must still log "file changed", not
// "file found", since the file was already known to Sublime.
func TestPipeline_ForceReprocessLogsFileChangedNotFileFound(t *testing.T) {
	libDir := t.TempDir()

	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
		Logger:      slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("first run: %v", err)
	}

	logBuf.Reset()
	result2, err := p.RunFile(ctx, lib, videoPath, pipeline.WithForce())
	if err != nil {
		t.Fatalf("forced RunFile: %v", err)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "file changed") {
		t.Errorf(`log output missing "file changed":%s`, logOutput)
	}
	if strings.Contains(logOutput, "file found") {
		t.Errorf(`log output unexpectedly contains "file found" for a forced reprocess of an already-tracked file:%s`, logOutput)
	}
	if result2.Synced != 1 {
		t.Errorf("forced run Synced = %d, want 1; errors: %v", result2.Synced, result2.Errors)
	}
}

// TestPipeline_StreamingDiscoveryRegistersPendingAheadOfWorkerPickup is the
// core regression test for this ticket: Pending must reflect the true
// discovered backlog as the Library's directory walk streams discoveries
// in, not a worker-count-bounded snapshot. With a single worker
// permanently stuck on its first Provider.Search call, every file in the
// Library must still be registered (Found, logged, and given a Pending row
// per language) well before that first job unblocks and completes.
func TestPipeline_StreamingDiscoveryRegistersPendingAheadOfWorkerPickup(t *testing.T) {
	libDir := t.TempDir()

	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}

	const numFiles = 8
	var videoPaths []string
	for i := 0; i < numFiles; i++ {
		videoPath := filepath.Join(libDir, fmt.Sprintf("Movie.%d.2020.HDTV.x264-GRP.mp4", i))
		if err := os.WriteFile(videoPath, videoContent, 0o644); err != nil {
			t.Fatalf("writing video %d: %v", i, err)
		}
		videoPaths = append(videoPaths, videoPath)
	}

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = st.Close() }()

	block := make(chan struct{})
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			<-block
			return nil, errors.New("search unblocked")
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English, language.Spanish},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	p := &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
		Logger:      slog.New(slog.NewTextHandler(&syncedWriter{mu: &logMu, buf: &logBuf}, nil)),
	}

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Run(ctx, lib); err != nil {
			t.Errorf("run: %v", err)
		}
	}()

	wantPending := numFiles * len(lib.Languages)
	deadline := time.Now().Add(5 * time.Second)
	var lastPending int
	for time.Now().Before(deadline) {
		summaries, err := st.LibrarySummaries(ctx)
		if err != nil {
			t.Fatalf("LibrarySummaries: %v", err)
		}
		for _, s := range summaries {
			if s.LibraryName == lib.Name {
				lastPending = s.Pending
			}
		}
		if lastPending == wantPending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if lastPending != wantPending {
		t.Fatalf("Pending = %d, want %d (streaming discovery must register every file ahead of worker pickup, with WorkerCount=1)", lastPending, wantPending)
	}

	logMu.Lock()
	foundCount := strings.Count(logBuf.String(), "file found")
	logMu.Unlock()
	if foundCount != numFiles {
		t.Errorf(`"file found" count = %d, want %d (one per file, not per language)`, foundCount, numFiles)
	}

	close(block)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline.Run did not finish after unblocking Provider.Search")
	}

	for _, videoPath := range videoPaths {
		file, found, err := st.GetFile(ctx, lib.Name, videoPath)
		if err != nil {
			t.Fatalf("getting file %q from store: %v", videoPath, err)
		}
		if !found {
			t.Errorf("file %q not found in store", videoPath)
		}
		if len(file.Languages) != len(lib.Languages) {
			t.Errorf("file %q Languages = %+v, want %d entries", videoPath, file.Languages, len(lib.Languages))
		}
	}
}

// syncedWriter serializes concurrent writes to buf behind mu, letting a
// slog.TextHandler be shared safely across the pipeline's per-file worker
// goroutines in tests.
type syncedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *syncedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

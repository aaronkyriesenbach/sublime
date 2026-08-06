package pipeline_test

import (
	"bytes"
	"context"
	"errors"
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

// syncedWriter serializes concurrent writes to buf behind mu, letting a
// slog.TextHandler be shared safely across goroutines in tests.
type syncedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *syncedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func writeVideoFixture(t *testing.T, dst string) {
	t.Helper()
	srcVideo := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	videoContent, err := os.ReadFile(srcVideo)
	if err != nil {
		t.Fatalf("reading source video: %v", err)
	}
	if err := os.WriteFile(dst, videoContent, 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// dispatchPending processes every currently Pending (file, language) pair
// for lib by calling pipeline.Pipeline.ProcessPending directly \u2014 the same
// seam a Dispatcher (internal/dispatcher) calls after claiming a pair from
// store.Store.PendingPairs. This is pipeline_test's stand-in for a real
// Dispatcher: it lets these tests observe the per-pair dispatch work
// (gate/claim/Search/Score/Download/Sync/Strip/outcome) that Run/RunFile no
// longer perform themselves \u2014 see
// docs/adr/0004-decouple-trigger-and-dispatcher.md.
func dispatchPending(t *testing.T, ctx context.Context, p *pipeline.Pipeline, st *store.Store, lib domain.Library) {
	t.Helper()
	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	for _, pair := range pairs {
		if pair.LibraryName != lib.Name {
			continue
		}
		result := p.ProcessPending(ctx, lib, pair.FileID, pair.ContentHash, pair.Path, pair.Language, pair.Force, "test-provider")

		// For single-provider tests, a no-candidate miss means exhaustion:
		// mark failed and log through p.Logger to match real Dispatcher behavior.
		if result.Outcome == pipeline.OutcomeNoCandidateMiss {
			if err := st.MarkFailed(ctx, pair.FileID, pair.Language, domain.FailureNoCandidate); err != nil {
				t.Fatalf("MarkFailed: %v", err)
			}
			if p.Logger != nil {
				p.Logger.Warn("status changed",
					"library", lib.Name,
					"path", pair.Path,
					"language", pair.Language.String(),
					"from", "in_progress",
					"to", "failed",
					"reason", "no_candidate")
			}
		}
	}
}

func TestPipeline_RunRegistersFoundFileAndFansOutPending(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{Store: st}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
	if result.Found != 1 {
		t.Errorf("Found = %d, want 1", result.Found)
	}
	if result.Changed != 0 {
		t.Errorf("Changed = %d, want 0", result.Changed)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found {
		t.Fatal("file not found in store")
	}
	if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("file.Languages = %+v, want 1 Pending entry", file.Languages)
	}
}

func TestPipeline_MultipleLanguagesFanOutOnePendingRowEach(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English, language.Spanish},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{Store: st}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || len(file.Languages) != 2 {
		t.Fatalf("file.Languages = %+v, want 2 entries", file.Languages)
	}
	for _, ls := range file.Languages {
		if ls.Status != domain.StatusPending {
			t.Errorf("language %q status = %q, want %q", ls.Language, ls.Status, domain.StatusPending)
		}
	}
}

func TestPipeline_ScanFindsVideoFilesAcrossSubdirectories(t *testing.T) {
	libDir := t.TempDir()
	subDir := filepath.Join(libDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	video1 := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	video2 := filepath.Join(subDir, "Movie.Two.2021.HDTV.x264-GRP.mkv")
	writeVideoFixture(t, video1)
	writeVideoFixture(t, video2)

	txtFile := filepath.Join(libDir, "readme.txt")
	if err := os.WriteFile(txtFile, []byte("not a video"), 0o644); err != nil {
		t.Fatalf("writing txt file: %v", err)
	}

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{Store: st}

	ctx := context.Background()
	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", result.FilesScanned)
	}
	if result.Found != 2 {
		t.Errorf("Found = %d, want 2", result.Found)
	}
}

func TestPipeline_RunFileRegistersOnlyTheGivenFile(t *testing.T) {
	libDir := t.TempDir()
	video1 := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	video2 := filepath.Join(libDir, "Movie.Two.2021.HDTV.x264-GRP.mp4")
	writeVideoFixture(t, video1)
	writeVideoFixture(t, video2)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{Store: st}

	ctx := context.Background()
	result, err := p.RunFile(ctx, lib, video1)
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}

	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
	if result.Found != 1 {
		t.Errorf("Found = %d, want 1", result.Found)
	}

	if _, found, err := st.GetFile(ctx, lib.Name, video1); err != nil || !found {
		t.Errorf("video1 not registered: found=%v err=%v", found, err)
	}
	if _, found, err := st.GetFile(ctx, lib.Name, video2); err != nil || found {
		t.Errorf("video2 unexpectedly registered: found=%v err=%v", found, err)
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
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{Store: st, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("first run: %v", err)
	}

	videoContent, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatalf("reading video: %v", err)
	}
	if err := os.WriteFile(videoPath, append(videoContent, []byte("mutated")...), 0o644); err != nil {
		t.Fatalf("mutating video: %v", err)
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
	if result2.Changed != 1 {
		t.Errorf("second run Changed = %d, want 1", result2.Changed)
	}
	if result2.Found != 0 {
		t.Errorf("second run Found = %d, want 0", result2.Found)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from store: %v", err)
	}
	if !found {
		t.Fatal("file not found in store")
	}
	if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("file.Languages = %+v, want 1 entry with status pending", file.Languages)
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
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English, language.Spanish},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{Store: st, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}

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

// TestPipeline_ForceReprocessLogsFileChangedAndMarksPairsForced guards a
// manual reprocess request (pipeline.WithForce) against an already-tracked
// file whose Content Hash hasn't changed: it must still log "file
// changed", not "file found", and mark every one of the file's pending
// pairs Force (store.PendingPair.Force) so a future Dispatcher bypasses the
// Marker+Content-Hash gate for it.
func TestPipeline_ForceReprocessLogsFileChangedAndMarksPairsForced(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{Store: st, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}

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
	if result2.Changed != 1 {
		t.Errorf("forced run Changed = %d, want 1", result2.Changed)
	}

	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != 1 || !pairs[0].Force {
		t.Fatalf("PendingPairs = %+v, want a single Force pair", pairs)
	}
}

// TestPipeline_RunFileAppliesSameFoundVsChangedClassificationAsRun guards
// the single-file entrypoint (fsnotify watch events, manual reprocessing)
// applying the identical Found-vs-Changed distinction as the bulk scan.
func TestPipeline_RunFileAppliesSameFoundVsChangedClassificationAsRun(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{Store: st, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}

	ctx := context.Background()

	if _, err := p.RunFile(ctx, lib, videoPath); err != nil {
		t.Fatalf("first RunFile: %v", err)
	}
	if got := strings.Count(logBuf.String(), "file found"); got != 1 {
		t.Errorf(`"file found" count = %d, want 1; log output:%s`, got, logBuf.String())
	}

	videoContent, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatalf("reading video: %v", err)
	}
	if err := os.WriteFile(videoPath, append(videoContent, []byte("mutated")...), 0o644); err != nil {
		t.Fatalf("mutating video: %v", err)
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

func TestPipeline_RunReturnsPromptlyWithoutWaitingOnAnyDispatch(t *testing.T) {
	libDir := t.TempDir()
	const numFiles = 8
	for i := 0; i < numFiles; i++ {
		writeVideoFixture(t, filepath.Join(libDir, fileNameForIndex(i)))
	}

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English, language.Spanish},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{Store: st}

	ctx := context.Background()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		if _, err := p.Run(ctx, lib); err != nil {
			t.Errorf("run: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly; it must never wait on any Dispatcher claiming or processing its Pending rows")
	}
	elapsed := time.Since(start)

	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != numFiles*len(lib.Languages) {
		t.Errorf("PendingPairs = %d, want %d (every discovered pair fanned out to Pending)", len(pairs), numFiles*len(lib.Languages))
	}
	t.Logf("Run took %s for %d files with no Provider configured", elapsed, numFiles)
}

func fileNameForIndex(i int) string {
	return "Movie." + string(rune('A'+i)) + ".2020.HDTV.x264-GRP.mp4"
}

// TestPipeline_ClaimLostSkipsWithoutError guards ProcessPending's handling
// of store.ErrClaimLost from MarkInProgress: when another caller has
// already claimed the (file, language) pair, ProcessPending must treat it
// like an already-synced skip (no error, no ERROR log), not a processing
// failure.
func TestPipeline_ClaimLostSkipsWithoutError(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	ctx := context.Background()
	en := language.English

	hash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		t.Fatalf("computing content hash: %v", err)
	}
	file, _, err := st.ObserveFileHash(ctx, "test-library", videoPath, string(hash))
	if err != nil {
		t.Fatalf("ObserveFileHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	// Simulate a concurrent caller having already claimed this pair before
	// this call reaches it.
	if err := st.MarkInProgress(ctx, file.ID, en); err != nil {
		t.Fatalf("pre-claiming MarkInProgress: %v", err)
	}

	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Search should not be called for a pair that lost its claim")
			return nil, nil
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{en},
		StripScope: domain.StripScopeAll,
	}

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	p := &pipeline.Pipeline{
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&syncedWriter{mu: &logMu, buf: &logBuf}, nil)),
	}

	result := p.ProcessPending(ctx, lib, file.ID, string(hash), videoPath, en, false, "test-provider")
	if result.Err != nil {
		t.Fatalf("ProcessPending: %v", result.Err)
	}

	logMu.Lock()
	logOutput := logBuf.String()
	logMu.Unlock()
	if strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("expected no ERROR log for a lost claim, got: %s", logOutput)
	}

	got, _, err := st.GetFile(ctx, "test-library", videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Languages[0].Status != domain.StatusInProgress {
		t.Errorf("expected the other claimant's status %q to survive untouched, got %q", domain.StatusInProgress, got.Languages[0].Status)
	}
}

// TestPipeline_MarkerGateFastPathSkipsInProgress guards the Marker-gate-hit
// fast path: when a valid Marker already exists (no Provider work needed),
// the (file, language) pair must transition directly from Pending to
// Synced, never touching In Progress — not even transiently.
func TestPipeline_MarkerGateFastPathSkipsInProgress(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	if err := os.WriteFile(videoPath, []byte("original video bytes"), 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	st := openTestStore(t)
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

	p := &pipeline.Pipeline{
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("first run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from store: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced after first dispatch, got %+v", file)
	}

	// Simulate total state loss: a brand-new, empty state store. The video
	// and its Marker-bearing sidecar are untouched on disk, so the Marker
	// gate alone must recognize it as already synced.
	freshStore := openTestStore(t)

	var logBuf bytes.Buffer
	p2 := &pipeline.Pipeline{
		Store:      freshStore,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	if _, err := p2.Run(ctx, lib); err != nil {
		t.Fatalf("second run: %v", err)
	}
	dispatchPending(t, ctx, p2, freshStore, lib)

	logOutput := logBuf.String()
	for _, want := range []string{`msg="status changed"`, "from=pending", "to=synced"} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}
	if got := strings.Count(logOutput, `msg="status changed"`); got != 1 {
		t.Errorf(`"status changed" count = %d, want exactly 1 (Pending->Synced fast path only):%s`, got, logOutput)
	}
	if strings.Contains(logOutput, "in_progress") {
		t.Errorf("log output unexpectedly mentions in_progress for the marker-gate fast path:%s", logOutput)
	}

	file2, found, err := freshStore.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from store: %v", err)
	}
	if !found {
		t.Fatal("file not found in store")
	}
	if len(file2.Languages) != 1 || file2.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("file.Languages = %+v, want 1 entry with status synced", file2.Languages)
	}
}

// TestPipeline_ContentHashReflectsPostStripState guards the Content Hash /
// Strip-ordering fix: when Strip's embedded-stream removal actually
// mutates the video, the Marker and store must bind to the file's settled
// post-Strip hash, not the pre-Strip hash captured before ProcessPending
// began.
func TestPipeline_ContentHashReflectsPostStripState(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	if err := os.WriteFile(videoPath, []byte("original video bytes"), 0o644); err != nil {
		t.Fatalf("writing video to library: %v", err)
	}

	st := openTestStore(t)
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
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   fakeStripper,
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("first run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	if len(fakeStripper.StripEmbeddedCalls) != 1 {
		t.Fatalf("StripEmbeddedCalls = %d, want 1", len(fakeStripper.StripEmbeddedCalls))
	}

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
	freshStore := openTestStore(t)

	fakeStripper.StripEmbeddedCalls = nil
	fakeSyncEngine := &syncengine.FakeSyncEngine{}
	p2 := &pipeline.Pipeline{
		Store:      freshStore,
		Provider:   fakeProvider,
		SyncEngine: fakeSyncEngine,
		Stripper:   fakeStripper,
	}

	if _, err := p2.Run(ctx, lib); err != nil {
		t.Fatalf("run against fresh store: %v", err)
	}
	dispatchPending(t, ctx, p2, freshStore, lib)

	file2, found, err := freshStore.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("getting file from fresh store: %v", err)
	}
	if !found || file2.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Marker-gate hit to leave file Synced, got %+v", file2)
	}
	if len(fakeSyncEngine.Calls) != 0 {
		t.Errorf("SyncEngine.Calls = %d, want 0 (should not reprocess)", len(fakeSyncEngine.Calls))
	}
	if len(fakeStripper.StripEmbeddedCalls) != 0 {
		t.Errorf("StripEmbeddedCalls = %d, want 0 (should not reprocess)", len(fakeStripper.StripEmbeddedCalls))
	}
}

// TestPipeline_LogsStatusChangeForRetrievalFailure guards the uniform
// "status changed" log line for a Failed landing whose reason isn't
// no_candidate: it must log at ERROR with from=in_progress, to=failed, and
// reason=retrieval_failed.
func TestPipeline_LogsStatusChangeForRetrievalFailure(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
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
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	logOutput := logBuf.String()
	for _, want := range []string{
		"level=ERROR", `msg="status changed"`, "library=test-library", "path=" + videoPath,
		"language=en", "from=in_progress", "to=failed", "reason=retrieval_failed",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusFailed || file.Languages[0].FailureReason != domain.FailureRetrievalFailed {
		t.Errorf("expected Failed/retrieval_failed, got %+v", file.Languages)
	}
}

// TestPipeline_QuotaExhaustedOnSearchResetsToPending guards that a
// provider.QuotaExhaustedError from Search leaves the (file, language) pair
// Pending rather than routing it through markFailed: the log line must
// land at INFO with to=pending and no reason, not an ERROR Failed landing.
func TestPipeline_QuotaExhaustedOnSearchResetsToPending(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	quotaErr := &provider.QuotaExhaustedError{ResumeAt: time.Now().Add(24 * time.Hour)}
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return nil, quotaErr
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
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	file, ok, err := st.GetFile(ctx, "test-library", videoPath)
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetFile to find the file")
	}
	if len(file.Languages) != 1 {
		t.Fatalf("expected exactly 1 language row, got %d: %+v", len(file.Languages), file.Languages)
	}
	if file.Languages[0].Status != domain.StatusPending {
		t.Errorf("expected language status %q, got %q", domain.StatusPending, file.Languages[0].Status)
	}
	if file.Languages[0].FailureReason != domain.FailureNone {
		t.Errorf("expected no failure reason, got %q", file.Languages[0].FailureReason)
	}

	logOutput := logBuf.String()
	for _, want := range []string{
		"level=INFO", `msg="status changed"`, "library=test-library", "path=" + videoPath,
		"language=en", "from=in_progress", "to=pending",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}
	if strings.Contains(logOutput, "to=failed") {
		t.Errorf("log output unexpectedly contains a Failed landing:%s", logOutput)
	}
	if strings.Contains(logOutput, "reason=") {
		t.Errorf("log output unexpectedly contains a failure reason:%s", logOutput)
	}
}

// TestPipeline_QuotaExhaustedOnDownloadResetsToPending guards the same
// behavior when the QuotaExhaustedError surfaces from Download instead of
// Search.
func TestPipeline_QuotaExhaustedOnDownloadResetsToPending(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	quotaErr := &provider.QuotaExhaustedError{ResumeAt: time.Now().Add(24 * time.Hour)}
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return nil, quotaErr
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
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	file, ok, err := st.GetFile(ctx, "test-library", videoPath)
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetFile to find the file")
	}
	if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusPending {
		t.Errorf("expected single Pending language row, got %+v", file.Languages)
	}
	if file.Languages[0].FailureReason != domain.FailureNone {
		t.Errorf("expected no failure reason, got %q", file.Languages[0].FailureReason)
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "to=failed") {
		t.Errorf("log output unexpectedly contains a Failed landing:%s", logOutput)
	}
}

// TestPipeline_LogsStatusChangeForNoCandidate guards the uniform "status
// changed" log line for a Failed landing whose reason is no_candidate: it
// must log at WARN (not ERROR) with from=in_progress, to=failed, and
// reason=no_candidate.
func TestPipeline_LogsStatusChangeForNoCandidate(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
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
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	logOutput := logBuf.String()
	for _, want := range []string{
		"level=WARN", `msg="status changed"`, "path=" + videoPath,
		"language=en", "from=in_progress", "to=failed", "reason=no_candidate",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusFailed || file.Languages[0].FailureReason != domain.FailureNoCandidate {
		t.Errorf("expected Failed/no_candidate, got %+v", file.Languages)
	}
}

// TestPipeline_TransitionsPendingToInProgressToSynced guards the state
// machine's normal path: dispatching a (file, language) pair transitions
// it to In Progress before its Marker gate check, then to Synced on
// success — each transition emitting exactly one uniform "status changed"
// log line at INFO.
func TestPipeline_TransitionsPendingToInProgressToSynced(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
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
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
		Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	logOutput := logBuf.String()
	for _, want := range []string{
		`msg="status changed"`, "from=pending", "to=in_progress",
		"from=in_progress", "to=synced",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}
	if got := strings.Count(logOutput, `msg="status changed"`); got != 2 {
		t.Errorf(`"status changed" count = %d, want 2 (pending->in_progress, in_progress->synced):%s`, got, logOutput)
	}
	inProgressIdx := strings.Index(logOutput, "to=in_progress")
	syncedIdx := strings.Index(logOutput, "to=synced")
	if inProgressIdx == -1 || syncedIdx == -1 || inProgressIdx > syncedIdx {
		t.Errorf("expected to=in_progress to be logged before to=synced:%s", logOutput)
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

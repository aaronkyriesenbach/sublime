package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/aaronkyriesenbach/sublime/internal/strip"
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
			if err := st.MarkFailed(ctx, pair.FileID, pair.ContentHash, pair.Language, domain.FailureNoCandidate); err != nil {
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

// TestIsVideoFile_ExcludesStripStrayTempFiles guards issue #81's phantom Found registration: Strip's own remux temp file carries a normal video extension.
func TestIsVideoFile_ExcludesStripStrayTempFiles(t *testing.T) {
	temp := strip.NewTempVideoPath(t.TempDir(), "Movie", ".mkv")
	if pipeline.IsVideoFile(temp) {
		t.Errorf("IsVideoFile(%q) = true, want false for Strip's own stray remux temp file", temp)
	}
}

// TestPipeline_ScanSkipsStripStrayTempFile guards issue #81's directory-walk case: a crash-orphaned Strip remux temp file on disk must never be registered as Found.
func TestPipeline_ScanSkipsStripStrayTempFile(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	writeVideoFixture(t, videoPath)

	strayPath := strip.NewTempVideoPath(libDir, "Movie.One.2020.HDTV.x264-GRP", ".mp4")
	writeVideoFixture(t, strayPath)

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
		t.Errorf("FilesScanned = %d, want 1 (stray temp file must not be scanned)", result.FilesScanned)
	}
	if result.Found != 1 {
		t.Errorf("Found = %d, want 1", result.Found)
	}

	if _, found, err := st.GetFile(ctx, lib.Name, strayPath); err != nil || found {
		t.Errorf("stray temp file registered: found=%v err=%v", found, err)
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

// TestPipeline_RunFile_DoesNotReconcileOtherTrackedFiles guards runFiles'
// walkErrCh-gating: RunFile only ever sees the one path it's given, so it
// must never treat every other tracked file in the Library as Removed.
func TestPipeline_RunFile_DoesNotReconcileOtherTrackedFiles(t *testing.T) {
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
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("initial Run: %v", err)
	}

	// RunFile re-registers only video1; video2 was never mentioned in this
	// call, but must remain tracked — RunFile isn't a Library scan.
	result, err := p.RunFile(ctx, lib, video1)
	if err != nil {
		t.Fatalf("RunFile: %v", err)
	}
	if result.Removed != 0 {
		t.Errorf("Removed = %d, want 0 (RunFile must never reconcile)", result.Removed)
	}

	if _, found, err := st.GetFile(ctx, lib.Name, video2); err != nil || !found {
		t.Errorf("video2 wrongly treated as Removed by RunFile: found=%v err=%v", found, err)
	}
}

// TestPipeline_Run_ReconcilesFileRemovedFromDisk guards the reconciliation
// safety net (CONTEXT.md's Removed entry): a Library scan must delete the
// row for a previously tracked file no longer present on disk, e.g. one
// deleted while nothing was watching.
func TestPipeline_Run_ReconcilesFileRemovedFromDisk(t *testing.T) {
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

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{Store: st, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("initial Run: %v", err)
	}

	if err := os.Remove(video2); err != nil {
		t.Fatalf("removing video2 fixture: %v", err)
	}

	result, err := p.Run(ctx, lib)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}

	if result.Removed != 1 {
		t.Errorf("Removed = %d, want 1", result.Removed)
	}
	if _, found, err := st.GetFile(ctx, lib.Name, video2); err != nil || found {
		t.Errorf("video2 still tracked after removal: found=%v err=%v", found, err)
	}
	if _, found, err := st.GetFile(ctx, lib.Name, video1); err != nil || !found {
		t.Errorf("video1 wrongly untracked: found=%v err=%v", found, err)
	}
	if got := strings.Count(logBuf.String(), "file removed"); got != 1 {
		t.Errorf(`"file removed" count = %d, want 1; log output:\n%s`, got, logBuf.String())
	}
}

func TestPipeline_RemoveFile_DeletesRowAndLogsOnce(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
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
		t.Fatalf("Run: %v", err)
	}
	logBuf.Reset()

	if err := p.RemoveFile(ctx, lib, videoPath); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}

	if _, found, err := st.GetFile(ctx, lib.Name, videoPath); err != nil || found {
		t.Errorf("GetFile after RemoveFile: found=%v err=%v, want found=false", found, err)
	}
	if got := strings.Count(logBuf.String(), "file removed"); got != 1 {
		t.Errorf(`"file removed" count = %d, want 1 (once per file, not per language); log output:\n%s`, got, logBuf.String())
	}
}

func TestPipeline_RemoveFile_AlreadyGoneDoesNotLog(t *testing.T) {
	st := openTestStore(t)
	lib := domain.Library{Name: "test-library", Path: t.TempDir(), Languages: []language.Tag{language.English}}

	var logBuf bytes.Buffer
	p := &pipeline.Pipeline{Store: st, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))}

	ctx := context.Background()
	if err := p.RemoveFile(ctx, lib, filepath.Join(lib.Path, "never-existed.mkv")); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}

	if logBuf.Len() != 0 {
		t.Errorf("expected no log output for an already-gone path, got:\n%s", logBuf.String())
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

// TestPipeline_UnavailableProviderResetsToPending guards that a
// provider.UnavailableError — from Search or from Download — leaves the pair
// Pending like quota exhaustion does, rather than Failed.
func TestPipeline_UnavailableProviderResetsToPending(t *testing.T) {
	unavailableErr := &provider.UnavailableError{ResumeAt: time.Now().Add(2 * time.Minute)}
	tests := []struct {
		name string
		fake *provider.Fake
	}{
		{
			name: "from Search",
			fake: &provider.Fake{
				SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
					return nil, unavailableErr
				},
			},
		},
		{
			name: "from Download",
			fake: &provider.Fake{
				SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
					return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
				},
				DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
					return nil, unavailableErr
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			libDir := t.TempDir()
			videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
			writeVideoFixture(t, videoPath)

			st := openTestStore(t)
			lib := domain.Library{
				Name:       "test-library",
				Path:       libDir,
				Languages:  []language.Tag{language.English},
				StripScope: domain.StripScopeAll,
			}
			p := &pipeline.Pipeline{
				Store:      st,
				Provider:   tc.fake,
				SyncEngine: &syncengine.FakeSyncEngine{},
				Stripper:   &pipeline.FakeStripper{},
			}

			ctx := context.Background()
			if _, err := p.Run(ctx, lib); err != nil {
				t.Fatalf("run: %v", err)
			}
			dispatchPending(t, ctx, p, st, lib)

			file, ok, err := st.GetFile(ctx, "test-library", videoPath)
			if err != nil || !ok {
				t.Fatalf("GetFile ok=%v err=%v", ok, err)
			}
			if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusPending {
				t.Fatalf("languages = %+v, want a single Pending row", file.Languages)
			}
			if file.Languages[0].FailureReason != domain.FailureNone {
				t.Errorf("failure reason = %q, want none", file.Languages[0].FailureReason)
			}
		})
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

// TestPipeline_LogsProviderSearchCandidateCount guards the debug-level
// per-search signal from docs/subdl-smoke-test-findings.md: every Search
// call must log its candidate count, so "the Provider found nothing" and
// "the Provider found candidates that all missed" are distinguishable from
// GET /status' single no_candidate outcome without an out-of-band request.
func TestPipeline_LogsProviderSearchCandidateCount(t *testing.T) {
	libDir := t.TempDir()
	videoName := "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"
	videoPath := filepath.Join(libDir, videoName)
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{
				{ID: "a", Title: "Wrong Title", Year: 1999},
				{ID: "b", Title: "Also Wrong", Year: 1998},
			}, nil
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
		Logger:     slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	logOutput := logBuf.String()
	for _, want := range []string{
		"level=DEBUG", `msg="provider search"`, "provider=test-provider",
		"path=" + videoPath, "language=en", "candidates=2",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
	}
}

// TestPipeline_LogsScoringMissTopCandidate guards the other half of the
// same debug signal: when nothing clears the eligibility cutoff, the log
// must carry the top-scoring miss's decoded identity fields and its score
// vs. cutoff — the exact data docs/subdl-smoke-test-findings.md says would
// have made the punctuation/title mismatch and wire-format bugs immediate
// instead of requiring a manual curl diff.
func TestPipeline_LogsScoringMissTopCandidate(t *testing.T) {
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
		Logger:     slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	logOutput := logBuf.String()
	for _, want := range []string{
		"level=DEBUG", `msg="scoring miss"`, "provider=test-provider",
		"path=" + videoPath, "language=en",
		`top_title="Wrong Title"`, "top_year=1999", "top_season=0", "top_episode=0",
		"score=0", "cutoff=96",
	} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output = %q, want it to contain %q", logOutput, want)
		}
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

// cancellingStripper cancels the pair's context while StripEmbedded runs, as
// a shutdown arriving mid-Strip would, and reports ctx's error the way an
// interrupted ffmpeg does.
type cancellingStripper struct {
	*pipeline.FakeStripper
	cancel context.CancelFunc
}

func (s *cancellingStripper) StripEmbedded(ctx context.Context, _ string, _ domain.StripScope, _ language.Tag) ([]int, error) {
	s.cancel()
	return nil, ctx.Err()
}

// cancellingSyncEngine cancels the pair's context while Sync runs and fails
// with a non-context error, since alass is killed or exits non-zero rather
// than reporting ctx's error.
type cancellingSyncEngine struct {
	cancel context.CancelFunc
}

func (e *cancellingSyncEngine) Sync(_, _, _ string) (string, error) {
	e.cancel()
	return "", errors.New("alass exited with status 1")
}

// TestPipeline_CancelledWorkLeavesPairPendingNotFailed guards that when
// shutdown cancels a pair's in-flight Search, Download, Sync or Strip, the
// pair ends Pending (retryable) with an info-level log, never Failed or
// stuck In Progress, even though ctx is already cancelled when the pair's
// status is written.
func TestPipeline_CancelledWorkLeavesPairPendingNotFailed(t *testing.T) {
	candidate := []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}
	srt := []byte("1\n00:00:00,500 --> 00:00:01,900\nSubtitle text\n")

	tests := []struct {
		name  string
		build func(cancel context.CancelFunc, p *pipeline.Pipeline)
	}{
		{
			name: "search",
			build: func(cancel context.CancelFunc, p *pipeline.Pipeline) {
				p.Provider = &provider.Fake{
					SearchFunc: func(ctx context.Context, _ provider.Query) ([]domain.Candidate, error) {
						cancel()
						return nil, ctx.Err()
					},
				}
			},
		},
		{
			name: "download",
			build: func(cancel context.CancelFunc, p *pipeline.Pipeline) {
				p.Provider = &provider.Fake{
					SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) { return candidate, nil },
					DownloadFunc: func(ctx context.Context, _ domain.Candidate) ([]byte, error) {
						cancel()
						return nil, fmt.Errorf("fetching subtitle: %w", ctx.Err())
					},
				}
			},
		},
		{
			name: "sync",
			build: func(cancel context.CancelFunc, p *pipeline.Pipeline) {
				p.Provider = &provider.Fake{
					SearchFunc:   func(context.Context, provider.Query) ([]domain.Candidate, error) { return candidate, nil },
					DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) { return srt, nil },
				}
				p.SyncEngine = &cancellingSyncEngine{cancel: cancel}
			},
		},
		{
			name: "strip",
			build: func(cancel context.CancelFunc, p *pipeline.Pipeline) {
				p.Provider = &provider.Fake{
					SearchFunc:   func(context.Context, provider.Query) ([]domain.Candidate, error) { return candidate, nil },
					DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) { return srt, nil },
				}
				p.Stripper = &cancellingStripper{FakeStripper: &pipeline.FakeStripper{}, cancel: cancel}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			libDir := t.TempDir()
			videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
			writeVideoFixture(t, videoPath)

			st := openTestStore(t)
			lib := domain.Library{
				Name:       "test-library",
				Path:       libDir,
				Languages:  []language.Tag{language.English},
				StripScope: domain.StripScopeAll,
			}

			var logBuf bytes.Buffer
			p := &pipeline.Pipeline{
				Store:      st,
				SyncEngine: &syncengine.FakeSyncEngine{},
				Stripper:   &pipeline.FakeStripper{},
				Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.build(cancel, p)

			if _, err := p.Run(context.Background(), lib); err != nil {
				t.Fatalf("run: %v", err)
			}
			file, _, err := st.GetFile(context.Background(), lib.Name, videoPath)
			if err != nil {
				t.Fatalf("GetFile: %v", err)
			}
			if err := st.RecordProviderMiss(context.Background(), file.ID, file.ContentHash, language.English, "earlier-provider"); err != nil {
				t.Fatalf("RecordProviderMiss: %v", err)
			}

			result := p.ProcessPending(ctx, lib, file.ID, file.ContentHash, videoPath, language.English, false, "test-provider")
			if result.Outcome != pipeline.OutcomePending {
				t.Errorf("outcome = %v (err %v), want OutcomePending", result.Outcome, result.Err)
			}

			got, _, err := st.GetFile(context.Background(), lib.Name, videoPath)
			if err != nil {
				t.Fatalf("GetFile: %v", err)
			}
			state := got.Languages[0]
			if state.Status != domain.StatusPending {
				t.Errorf("status = %q, want pending", state.Status)
			}
			if state.FailureReason != domain.FailureNone {
				t.Errorf("failure reason = %q, want none", state.FailureReason)
			}
			if !slices.Equal(state.Attempted, []string{"earlier-provider"}) {
				t.Errorf("attempted = %v, want the earlier provider kept", state.Attempted)
			}

			logOutput := logBuf.String()
			for _, want := range []string{"level=INFO", `msg="status changed"`, "from=in_progress", "to=pending"} {
				if !strings.Contains(logOutput, want) {
					t.Errorf("log output = %q, want it to contain %q", logOutput, want)
				}
			}
			if strings.Contains(logOutput, "to=failed") || strings.Contains(logOutput, "level=ERROR") {
				t.Errorf("log output unexpectedly records a failure:%s", logOutput)
			}
		})
	}
}

// TestPipeline_GenuineFailureStillFailsWhenContextIsLive guards that only
// cancellation is treated as an interruption: a Provider error with a live
// context still lands Failed(retrieval_failed).
func TestPipeline_GenuineFailureStillFailsWhenContextIsLive(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{
		Store: st,
		Provider: &provider.Fake{
			SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
				return nil, errors.New("provider returned 500")
			},
		},
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	got, _, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Languages[0].Status != domain.StatusFailed || got.Languages[0].FailureReason != domain.FailureRetrievalFailed {
		t.Errorf("state = %+v, want failed/retrieval_failed", got.Languages[0])
	}
}

// TestPipeline_InterruptedForcedPairIsStillForcedWhenClaimedAgain guards that
// a forced reprocess cut short by cancellation bypasses the Marker gate on
// its next claim, even though a valid Marker already exists on disk.
func TestPipeline_InterruptedForcedPairIsStillForcedWhenClaimedAgain(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	srt := []byte("1\n00:00:00,500 --> 00:00:01,900\nSubtitle text\n")
	var searches int
	p := &pipeline.Pipeline{
		Store: st,
		Provider: &provider.Fake{
			SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
				searches++
				return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
			},
			DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) { return srt, nil },
		},
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	// First pass syncs normally, leaving a valid Marker behind.
	dispatchPending(t, ctx, p, st, lib)
	if searches != 1 {
		t.Fatalf("searches after first pass = %d, want 1", searches)
	}

	if _, err := p.RunFile(ctx, lib, videoPath, pipeline.WithForce()); err != nil {
		t.Fatalf("forced RunFile: %v", err)
	}

	// The forced attempt is interrupted mid-Search.
	cancelCtx, cancel := context.WithCancel(ctx)
	p.Provider = &provider.Fake{
		SearchFunc: func(c context.Context, _ provider.Query) ([]domain.Candidate, error) {
			cancel()
			return nil, c.Err()
		},
	}
	dispatchPending(t, cancelCtx, p, st, lib)

	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != 1 || !pairs[0].Force {
		t.Fatalf("PendingPairs = %+v, want one forced pair after interruption", pairs)
	}

	// Claimed again, the forced pair runs the Provider despite the Marker.
	p.Provider = &provider.Fake{
		SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
			searches++
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) { return srt, nil },
	}
	dispatchPending(t, ctx, p, st, lib)
	if searches != 2 {
		t.Errorf("searches after reclaim = %d, want 2 (Marker gate bypassed)", searches)
	}
	got, _, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Languages[0].Status != domain.StatusSynced {
		t.Errorf("status = %q, want synced", got.Languages[0].Status)
	}
}

// neverSyncedFixture wires a Pipeline around a provider.Fake that declares
// GeneratesSubtitles (or not), with a fake Sync Engine whose Calls tell a
// test whether the sync stage ran.
type neverSyncedFixture struct {
	lib       domain.Library
	videoPath string
	sidecar   string
	st        *store.Store
	engine    *syncengine.FakeSyncEngine
	downloads *int
	pipe      *pipeline.Pipeline
}

func newNeverSyncedFixture(t *testing.T, generatesSubtitles bool) *neverSyncedFixture {
	t.Helper()
	return newNeverSyncedFixtureNamed(t, generatesSubtitles, "Test.Movie.2024.HDTV.x264-FAKEGROUP")
}

func newNeverSyncedFixtureNamed(t *testing.T, generatesSubtitles bool, baseName string) *neverSyncedFixture {
	t.Helper()
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, baseName+".mp4")
	writeVideoFixture(t, videoPath)

	downloads := new(int)
	fakeProvider := &provider.Fake{
		GeneratesSubtitles: generatesSubtitles,
		SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
			*downloads++
			return []byte("1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"), nil
		},
	}
	st := openTestStore(t)
	engine := &syncengine.FakeSyncEngine{}
	return &neverSyncedFixture{
		lib: domain.Library{
			Name:       "test-library",
			Path:       libDir,
			Languages:  []language.Tag{language.English},
			StripScope: domain.StripScopeAll,
		},
		videoPath: videoPath,
		sidecar:   filepath.Join(libDir, baseName+".en.srt"),
		st:        st,
		engine:    engine,
		downloads: downloads,
		pipe: &pipeline.Pipeline{
			Store:      st,
			Provider:   fakeProvider,
			SyncEngine: engine,
			Stripper:   &pipeline.FakeStripper{},
		},
	}
}

func (f *neverSyncedFixture) assertSyncedWithValidMarker(t *testing.T) {
	t.Helper()
	file, found, err := f.st.GetFile(context.Background(), f.lib.Name, f.videoPath)
	if err != nil || !found {
		t.Fatalf("GetFile found=%v err=%v", found, err)
	}
	if len(file.Languages) != 1 || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("file.Languages = %+v, want 1 entry with status synced", file.Languages)
	}

	content, err := os.ReadFile(f.sidecar)
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}
	hash, err := media.ComputeContentHash(f.videoPath)
	if err != nil {
		t.Fatalf("computing video hash: %v", err)
	}
	codec, _ := marker.CodecFor(".srt")
	m, presence := codec.Read(content)
	if presence != marker.Present || m.ContentHash != hash {
		t.Fatalf("sidecar marker = %+v (presence %v), want hash %q Present", m, presence, hash)
	}
}

func TestPipeline_NeverSyncedProviderSkipsSyncAndEndsSyncedWithMarker(t *testing.T) {
	f := newNeverSyncedFixture(t, true)
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)

	if len(f.engine.Calls) != 0 {
		t.Errorf("SyncEngine.Calls = %d, want 0 for a never-Synced Provider", len(f.engine.Calls))
	}
	f.assertSyncedWithValidMarker(t)
}

// A generated subtitle depends only on the video's own audio, so a filename
// the scorer cannot parse (a TV special such as "S10ES") must not fail it.
func TestPipeline_NeverSyncedProviderIgnoresAnUnparseableFilename(t *testing.T) {
	f := newNeverSyncedFixtureNamed(t, true, "Some Show - Twice Upon a Time")
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)

	f.assertSyncedWithValidMarker(t)
}

func TestPipeline_OrdinaryProviderStillFailsOnAnUnparseableFilename(t *testing.T) {
	f := newNeverSyncedFixtureNamed(t, false, "Some Show - Twice Upon a Time")
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)

	file, found, err := f.st.GetFile(ctx, f.lib.Name, f.videoPath)
	if err != nil || !found {
		t.Fatalf("GetFile found=%v err=%v", found, err)
	}
	if got := file.Languages[0]; got.Status != domain.StatusFailed || got.FailureReason != domain.FailureInternalError {
		t.Errorf("state = %q/%q, want failed/internal_error", got.Status, got.FailureReason)
	}
}

func TestPipeline_DownloadMissRecordsProviderTriedAndWritesNothing(t *testing.T) {
	f := newNeverSyncedFixture(t, true)
	f.pipe.Provider = &provider.Fake{
		SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024, HashMatch: true}}, nil
		},
		DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
			return nil, &provider.MissError{Cause: errors.New("transcript is degenerate")}
		},
	}
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	file, _, err := f.st.GetFile(ctx, f.lib.Name, f.videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}

	result := f.pipe.ProcessPending(ctx, f.lib, file.ID, file.ContentHash, f.videoPath, language.English, false, "whisper")

	if result.Outcome != pipeline.OutcomeNoCandidateMiss {
		t.Errorf("outcome = %v (err %v), want OutcomeNoCandidateMiss", result.Outcome, result.Err)
	}
	got, _, err := f.st.GetFile(ctx, f.lib.Name, f.videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	state := got.Languages[0]
	if state.Status != domain.StatusPending || state.FailureReason != domain.FailureNone {
		t.Errorf("state = %q/%q, want pending with no failure reason", state.Status, state.FailureReason)
	}
	if !slices.Equal(state.Attempted, []string{"whisper"}) {
		t.Errorf("attempted = %v, want [whisper]", state.Attempted)
	}
	if _, err := os.Stat(f.sidecar); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sidecar stat error = %v, want it not to exist", err)
	}
}

func TestPipeline_NeverSyncedProviderStillRebindsMarkerToPostStripHash(t *testing.T) {
	f := newNeverSyncedFixture(t, true)
	f.pipe.Stripper = &pipeline.FakeStripper{StripEmbeddedIndices: []int{2}}
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)

	if len(f.engine.Calls) != 0 {
		t.Errorf("SyncEngine.Calls = %d, want 0", len(f.engine.Calls))
	}
	f.assertSyncedWithValidMarker(t)
}

func TestPipeline_OrdinaryProviderStillSyncs(t *testing.T) {
	f := newNeverSyncedFixture(t, false)
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)

	if len(f.engine.Calls) != 1 {
		t.Errorf("SyncEngine.Calls = %d, want 1 for an ordinary Provider", len(f.engine.Calls))
	}
	f.assertSyncedWithValidMarker(t)
}

func TestPipeline_NeverSyncedProviderHonorsMarkerGateAndForce(t *testing.T) {
	f := newNeverSyncedFixture(t, true)
	ctx := context.Background()
	if _, err := f.pipe.Run(ctx, f.lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)
	if *f.downloads != 1 {
		t.Fatalf("downloads after first dispatch = %d, want 1", *f.downloads)
	}

	// A re-registered pair with a still-valid Marker is skipped by the gate.
	file, found, err := f.st.GetFile(ctx, f.lib.Name, f.videoPath)
	if err != nil || !found {
		t.Fatalf("GetFile found=%v err=%v", found, err)
	}
	if err := f.st.ResetToPending(ctx, file.ID); err != nil {
		t.Fatalf("resetting to pending: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)
	if *f.downloads != 1 {
		t.Errorf("downloads after gated dispatch = %d, want still 1", *f.downloads)
	}

	// Force bypasses the gate and regenerates, still without Sync.
	if _, err := f.pipe.RunFile(ctx, f.lib, f.videoPath, pipeline.WithForce()); err != nil {
		t.Fatalf("forced RunFile: %v", err)
	}
	dispatchPending(t, ctx, f.pipe, f.st, f.lib)
	if *f.downloads != 2 {
		t.Errorf("downloads after forced dispatch = %d, want 2", *f.downloads)
	}
	if len(f.engine.Calls) != 0 {
		t.Errorf("SyncEngine.Calls = %d, want 0", len(f.engine.Calls))
	}
	f.assertSyncedWithValidMarker(t)
}

package dispatcher_test

import (
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

	"github.com/aaronkyriesenbach/sublime/internal/dispatcher"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

func writeVideoFixture(t *testing.T, dst string) {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading sample video: %v", err)
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		t.Fatalf("writing video %q: %v", dst, err)
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

// TestDispatcher_RunOnceClaimsAndSyncsPendingPairs is the seam analogous to
// pipeline_test.go's TestPipeline_EndToEnd before this ticket: a Trigger
// (pipeline.Pipeline.Run) registers a file to Pending, then a single
// Dispatcher.RunOnce pass claims and syncs it \u2014 observable only via the
// real Store, never via any Dispatcher-internal state.
func TestDispatcher_RunOnceClaimsAndSyncsPendingPairs(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
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

	p := &pipeline.Pipeline{
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile before dispatch: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("expected Pending after registration, got %+v", file)
	}

	d := &dispatcher.Dispatcher{Pipeline: p, Store: st, Libraries: []domain.Library{lib}}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	file, found, err = st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile after dispatch: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced after RunOnce, got %+v", file)
	}
}

// TestDispatcher_MarkerGateHitSkipsClaimEntirely guards that a
// Marker+Content-Hash gate hit goes straight from Pending to Synced
// without ever passing through In Progress, even when routed through a
// Dispatcher pass rather than called directly.
func TestDispatcher_MarkerGateHitSkipsClaimEntirely(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
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

	p := &pipeline.Pipeline{
		Store:      st,
		Provider:   fakeProvider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	d := &dispatcher.Dispatcher{Pipeline: p, Store: st, Libraries: []domain.Library{lib}}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}

	// A brand-new, empty state store: the on-disk Marker alone must be
	// enough to recognize the file as already synced.
	freshStore := openTestStore(t)
	var logBuf strings.Builder
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

	d2 := &dispatcher.Dispatcher{Pipeline: p2, Store: freshStore, Libraries: []domain.Library{lib}}
	if err := d2.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}

	if strings.Contains(logBuf.String(), "in_progress") {
		t.Errorf("log output unexpectedly mentions in_progress for a marker-gate hit:%s", logBuf.String())
	}

	file, found, err := freshStore.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced via marker gate, got %+v", file)
	}
}

// TestDispatcher_RunOnceIsBoundedByPipelineWorkerCount guards the
// centralized-worker-pool fix: with Pipeline.WorkerCount set to 1, only one
// (file, language) pair may be claimed (In Progress) concurrently across
// every Library RunOnce dispatches, no matter how many Libraries or Pending
// pairs exist.
func TestDispatcher_RunOnceIsBoundedByPipelineWorkerCount(t *testing.T) {
	libDir := t.TempDir()
	const numFiles = 6
	for i := 0; i < numFiles; i++ {
		writeVideoFixture(t, filepath.Join(libDir, string(rune('A'+i))+".Movie.2020.HDTV.x264-GRP.mp4"))
	}

	st := openTestStore(t)
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
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	d := &dispatcher.Dispatcher{Pipeline: p, Store: st, Libraries: []domain.Library{lib}}
	done := make(chan error, 1)
	go func() { done <- d.RunOnce(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	var lastPending, lastInProgress int
	for time.Now().Before(deadline) {
		summaries, err := st.LibrarySummaries(ctx)
		if err != nil {
			t.Fatalf("LibrarySummaries: %v", err)
		}
		for _, s := range summaries {
			if s.LibraryName == lib.Name {
				lastPending = s.Pending
				lastInProgress = s.InProgress
			}
		}
		if lastInProgress == 1 && lastPending == numFiles-1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if lastInProgress != 1 {
		t.Fatalf("InProgress = %d, want 1 (bounded by Pipeline.WorkerCount=1)", lastInProgress)
	}
	if lastPending != numFiles-1 {
		t.Fatalf("Pending = %d, want %d", lastPending, numFiles-1)
	}

	close(block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not finish after unblocking Provider.Search")
	}
}

// TestDispatcher_UnconfiguredLibraryIsSkippedNotFatal guards that a
// Pending pair whose Library name isn't present in Dispatcher.Libraries
// (e.g. removed from config since it was registered) is skipped with a
// warning rather than aborting the whole pass.
func TestDispatcher_UnconfiguredLibraryIsSkippedNotFatal(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Search should not be called for an unconfigured library's pair")
			return nil, nil
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
		t.Fatalf("run: %v", err)
	}

	var logBuf strings.Builder
	d := &dispatcher.Dispatcher{
		Pipeline:  p,
		Store:     st,
		Libraries: nil, // lib isn't configured for this Dispatcher
		Logger:    slog.New(slog.NewTextHandler(&logBuf, nil)),
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if !strings.Contains(logBuf.String(), "unconfigured library") {
		t.Errorf("expected an unconfigured-library warning, got: %s", logBuf.String())
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusPending {
		t.Errorf("expected the pair to remain Pending (untouched), got %+v", file)
	}
}

// TestDispatcher_RunLoopsOnPollIntervalAndHonorsCancellation is the one
// lightweight test for Run's thin polling-ticker wrapper: with no
// Libraries configured, every pass logs its "unconfigured library" warning
// for the one Pending pair on disk, so counting those log lines across a
// short window proves Run ticks more than once, and Run must return
// promptly once ctx is cancelled.
func TestDispatcher_RunLoopsOnPollIntervalAndHonorsCancellation(t *testing.T) {
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

	p := &pipeline.Pipeline{Store: st}
	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	var mu sync.Mutex
	var logBuf strings.Builder
	d := &dispatcher.Dispatcher{
		Pipeline:     p,
		Store:        st,
		Libraries:    nil,
		PollInterval: 10 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(&syncedWriter{mu: &mu, buf: &logBuf}, nil)),
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.Run(runCtx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := strings.Count(logBuf.String(), "unconfigured library")
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	count := strings.Count(logBuf.String(), "unconfigured library")
	mu.Unlock()
	if count < 2 {
		t.Fatalf("saw %d passes within the deadline, want at least 2 (Run must loop on PollInterval)", count)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run() error = nil, want context.Canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// syncedWriter serializes concurrent writes to buf behind mu, letting a
// slog.TextHandler be shared safely across the Dispatcher's own goroutine
// and the polling ticker in tests.
type syncedWriter struct {
	mu  *sync.Mutex
	buf *strings.Builder
}

func (w *syncedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

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

// TestDispatcher_SuspendedProviderLeavesPendingUntouched guards issue #64's
// fix: a Pending pair whose Provider is currently Suspended is left
// Pending — never claimed, never Failed — and is picked up on a later
// RunOnce pass once Suspension() reports availability again, observable
// only via the real Store's Sync Status transitions.
func TestDispatcher_SuspendedProviderLeavesPendingUntouched(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	suspended := true
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			if suspended {
				t.Fatal("Search should not be called while the Provider is Suspended")
			}
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Now().Add(time.Hour), suspended
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
		t.Fatalf("RunOnce while suspended: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile while suspended: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("expected the pair to remain Pending while Suspended, got %+v", file)
	}

	suspended = false
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce after resuming: %v", err)
	}

	file, found, err = st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile after resuming: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected the pair to be Synced after the Provider resumed, got %+v", file)
	}
}

// TestDispatcher_AvailableProviderIsUnaffectedBySuspensionCheck guards that
// a Provider reporting not-suspended dispatches exactly as before — the
// new gate never blocks a healthy Provider.
func TestDispatcher_AvailableProviderIsUnaffectedBySuspensionCheck(t *testing.T) {
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
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
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
		t.Fatalf("RunOnce: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced after RunOnce with an available Provider, got %+v", file)
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

// TestDispatcher_ProviderChainSkipsSuspendedProviderAndDispatchesViaNext
// guards that when Provider #1 in the chain is Suspended, the Dispatcher
// walks the chain and dispatches via Provider #2 instead of leaving the
// pair Pending. Observable via the real Store's Sync Status transitions.
func TestDispatcher_ProviderChainSkipsSuspendedProviderAndDispatchesViaNext(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"

	// Provider #1: always Suspended, should never be called
	provider1 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Provider #1 Search should not be called while Suspended")
			return nil, nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Now().Add(time.Hour), true
		},
	}

	// Provider #2: available, should handle the dispatch
	provider2 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	// Create two pipelines, one per provider, sharing everything except Provider
	pipeline1 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider1,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}
	pipeline2 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider2,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	// Use pipeline1 for registration (it doesn't call the Provider)
	ctx := context.Background()
	if _, err := pipeline1.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile before dispatch: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("expected Pending after registration, got %+v", file)
	}

	// Dispatcher with a two-provider chain: provider1 (Suspended) -> provider2 (available)
	d := &dispatcher.Dispatcher{
		Providers: []dispatcher.ProviderEntry{
			{Pipeline: pipeline1, WorkerCount: 2},
			{Pipeline: pipeline2, WorkerCount: 2},
		},
		Store:     st,
		Libraries: []domain.Library{lib},
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	file, found, err = st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile after dispatch: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced after RunOnce (via Provider #2), got %+v", file)
	}
}

// TestDispatcher_ProviderChainPerProviderWorkerPoolBudget guards that each
// Provider in the chain has its own independent worker pool budget, so a
// slow/blocked Provider #1 doesn't starve dispatch capacity for Provider #2.
func TestDispatcher_ProviderChainPerProviderWorkerPoolBudget(t *testing.T) {
	libDir := t.TempDir()
	// Create 4 video files to exercise concurrency
	for i := 0; i < 4; i++ {
		writeVideoFixture(t, filepath.Join(libDir, string(rune('A'+i))+".Movie.2020.HDTV.x264-GRP.mp4"))
	}

	st := openTestStore(t)
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"

	// Provider #1: blocks forever until unblocked, simulating a slow provider
	block1 := make(chan struct{})
	provider1 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			<-block1
			return nil, errors.New("provider1 unblocked")
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false // not suspended, just slow
		},
	}

	// Provider #2: fast, available provider
	provider2 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", HashMatch: true}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	pipeline1 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider1,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}
	pipeline2 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider2,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := pipeline1.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Dispatcher with per-provider worker counts:
	// Provider #1 gets 1 worker, Provider #2 gets 2 workers
	// Provider #1 blocks its 1 worker; Provider #2 should still process 2 at a time
	d := &dispatcher.Dispatcher{
		Providers: []dispatcher.ProviderEntry{
			{Pipeline: pipeline1, WorkerCount: 1},
			{Pipeline: pipeline2, WorkerCount: 2},
		},
		Store:     st,
		Libraries: []domain.Library{lib},
	}

	done := make(chan error, 1)
	go func() { done <- d.RunOnce(ctx) }()

	// Wait for Provider #1 to consume its 1 worker slot (blocking on Search)
	// and for Provider #2 to process some files. Since Provider #1 is blocked,
	// Provider #2 should still be able to dispatch with its own 2-worker budget.
	deadline := time.Now().Add(5 * time.Second)
	var lastSynced int
	for time.Now().Before(deadline) {
		summaries, err := st.LibrarySummaries(ctx)
		if err != nil {
			t.Fatalf("LibrarySummaries: %v", err)
		}
		for _, s := range summaries {
			if s.LibraryName == lib.Name {
				lastSynced = s.Synced
			}
		}
		// Provider #1 claims 1 file (blocked), Provider #2 should process 3.
		// If per-provider pools are independent, we expect at least 2 Synced
		// while Provider #1 is still blocked on its 1 file.
		if lastSynced >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if lastSynced < 2 {
		t.Fatalf("expected at least 2 Synced via Provider #2 while Provider #1 is blocked, got %d", lastSynced)
	}

	// Unblock Provider #1 so the test can finish
	close(block1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not finish after unblocking Provider #1")
	}
}

// TestDispatcher_TierScopedSuspension_SkipsToTierMate guards that when a
// Provider in a Tier is Suspended, the Dispatcher skips to a non-Suspended
// Tier-mate within the same Tier, rather than leaving the pair Pending.
func TestDispatcher_TierScopedSuspension_SkipsToTierMate(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"

	// Provider #1: Suspended, should be skipped
	provider1 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Provider #1 Search should not be called while Suspended")
			return nil, nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Now().Add(time.Hour), true
		},
	}

	// Provider #2: available Tier-mate, should handle the dispatch
	provider2 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	pipeline1 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider1,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}
	pipeline2 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider2,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := pipeline1.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Both providers in the SAME Tier — Tier-mate skip should work
	d := &dispatcher.Dispatcher{
		ProviderTiers: []dispatcher.ProviderTier{{
			Providers: []dispatcher.ProviderEntry{
				{Name: "provider1", Pipeline: pipeline1, WorkerCount: 2},
				{Name: "provider2", Pipeline: pipeline2, WorkerCount: 2},
			},
		}},
		Store:     st,
		Libraries: []domain.Library{lib},
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced via Tier-mate, got %+v", file)
	}
}

// TestDispatcher_TierScopedSuspension_PrefersFirstHealthyProvider guards that
// within a Tier, the first non-Suspended Provider is always preferred, even
// when a later Tier-mate also has spare capacity.
func TestDispatcher_TierScopedSuspension_PrefersFirstHealthyProvider(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"

	// Track which provider actually handled the dispatch
	var handledBy string
	var mu sync.Mutex

	// Provider #1: healthy, should be preferred
	provider1 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			mu.Lock()
			handledBy = "provider1"
			mu.Unlock()
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
		},
	}

	// Provider #2: also healthy with spare capacity, but should NOT be used
	provider2 := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			mu.Lock()
			handledBy = "provider2"
			mu.Unlock()
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	pipeline1 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider1,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}
	pipeline2 := &pipeline.Pipeline{
		Store:      st,
		Provider:   provider2,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := pipeline1.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Both providers in the SAME Tier, both healthy with spare capacity
	d := &dispatcher.Dispatcher{
		ProviderTiers: []dispatcher.ProviderTier{{
			Providers: []dispatcher.ProviderEntry{
				{Name: "provider1", Pipeline: pipeline1, WorkerCount: 2},
				{Name: "provider2", Pipeline: pipeline2, WorkerCount: 2},
			},
		}},
		Store:     st,
		Libraries: []domain.Library{lib},
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	mu.Lock()
	result := handledBy
	mu.Unlock()

	if result != "provider1" {
		t.Fatalf("expected dispatch via provider1 (first healthy), got %q", result)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected Synced, got %+v", file)
	}
}

// TestDispatcher_TierScopedSuspension_AllTier1SuspendedWaitsOnTier1 guards
// that when every Provider in Tier 1 is Suspended, a Pending pair stays
// Pending waiting on Tier 1 — it does NOT fall through to an available
// Tier 2.
func TestDispatcher_TierScopedSuspension_AllTier1SuspendedWaitsOnTier1(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)
	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"

	// Tier 1 Provider: Suspended
	tier1Provider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Tier 1 Provider Search should not be called while Suspended")
			return nil, nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Now().Add(time.Hour), true
		},
	}

	// Tier 2 Provider: available, but should NOT be used
	tier2Provider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Tier 2 Provider Search should not be called when Tier 1 is Suspended")
			return []domain.Candidate{{ID: "candidate", Title: "Test Movie", Year: 2024}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Time{}, false
		},
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	tier1Pipeline := &pipeline.Pipeline{
		Store:      st,
		Provider:   tier1Provider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}
	tier2Pipeline := &pipeline.Pipeline{
		Store:      st,
		Provider:   tier2Provider,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := tier1Pipeline.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Two separate Tiers: Tier 1 (Suspended), Tier 2 (available)
	d := &dispatcher.Dispatcher{
		ProviderTiers: []dispatcher.ProviderTier{
			{Providers: []dispatcher.ProviderEntry{
				{Name: "tier1-provider", Pipeline: tier1Pipeline, WorkerCount: 2},
			}},
			{Providers: []dispatcher.ProviderEntry{
				{Name: "tier2-provider", Pipeline: tier2Pipeline, WorkerCount: 2},
			}},
		},
		Store:     st,
		Libraries: []domain.Library{lib},
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Pair should stay Pending, waiting on Tier 1
	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("expected pair to stay Pending (waiting on Tier 1), got %+v", file)
	}
}

// TestDispatcher_TierScopedSuspension_SingleProviderTierBehavesLikeLegacy
// guards that a single-Provider Tier behaves exactly like the legacy
// single-entry-chain: a Suspended Provider leaves the pair waiting.
func TestDispatcher_TierScopedSuspension_SingleProviderTierBehavesLikeLegacy(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeVideoFixture(t, videoPath)

	st := openTestStore(t)

	// Single provider: Suspended
	prov := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			t.Fatal("Search should not be called while Suspended")
			return nil, nil
		},
		SuspendedFunc: func() (time.Time, bool) {
			return time.Now().Add(time.Hour), true
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
		Provider:   prov,
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Single-Provider Tier
	d := &dispatcher.Dispatcher{
		ProviderTiers: []dispatcher.ProviderTier{{
			Providers: []dispatcher.ProviderEntry{
				{Name: "provider", Pipeline: p, WorkerCount: 2},
			},
		}},
		Store:     st,
		Libraries: []domain.Library{lib},
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusPending {
		t.Fatalf("expected pair to stay Pending, got %+v", file)
	}
}

package trigger_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/trigger"
)

// syncedLogBuffer serializes writes so a slog.TextHandler can be shared across goroutines.
type syncedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncedLogBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncedLogBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForPending polls st for videoPath's Sync Status to show up as
// Pending, failing the test if it doesn't within timeout. Watch/scan side
// effects land asynchronously relative to the test goroutine, so a fixed
// sleep would be both slow and flaky.
func waitForPending(t *testing.T, ctx context.Context, st interface {
	GetFile(ctx context.Context, libraryName, path string) (domain.File, bool, error)
}, libraryName, path string, timeout time.Duration) domain.File {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		file, found, err := st.GetFile(ctx, libraryName, path)
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if found && len(file.Languages) > 0 {
			return file
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q to be registered", path)
	return domain.File{}
}

func TestWatcher_InitialScanRegistersPreExistingFiles(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeSampleVideo(t, videoPath)

	p, st := newTestPipeline(t, nil)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	w := &trigger.Watcher{
		Pipeline:         p,
		Library:          lib,
		DebounceInterval: 20 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	file := waitForPending(t, ctx, st, lib.Name, videoPath, 2*time.Second)
	if file.Languages[0].Status != domain.StatusPending {
		t.Errorf("status = %q, want %q (Watcher only registers, never dispatches)", file.Languages[0].Status, domain.StatusPending)
	}
}

func TestWatcher_LiveFileCreationEventIsRegistered(t *testing.T) {
	libDir := t.TempDir()

	p, st := newTestPipeline(t, nil)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ready := make(chan struct{})
	w := &trigger.Watcher{
		Pipeline:         p,
		Library:          lib,
		DebounceInterval: 20 * time.Millisecond,
		OnWatching:       func() { close(ready) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not become ready in time")
	}

	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeSampleVideo(t, videoPath)

	file := waitForPending(t, ctx, st, lib.Name, videoPath, 2*time.Second)
	if file.Languages[0].Status != domain.StatusPending {
		t.Errorf("status = %q, want %q (Watcher only registers, never dispatches)", file.Languages[0].Status, domain.StatusPending)
	}
}

// TestWatcher_StripStrayTempFileEventIsIgnored is issue #81's regression test: a stray Strip temp file's fsnotify event must never reach registration.
func TestWatcher_StripStrayTempFileEventIsIgnored(t *testing.T) {
	libDir := t.TempDir()

	p, _ := newTestPipeline(t, nil)
	logBuf := &syncedLogBuffer{}
	p.Logger = slog.New(slog.NewTextHandler(logBuf, nil))
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ready := make(chan struct{})
	w := &trigger.Watcher{
		Pipeline:         p,
		Library:          lib,
		DebounceInterval: 20 * time.Millisecond,
		OnWatching:       func() { close(ready) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not become ready in time")
	}

	// Simulates StripEmbedded's remux temp file vanishing via rename before its debounce window elapses — the exact timing behind #81's phantom Found bug.
	strayPath := strip.NewTempVideoPath(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP", ".mp4")
	writeSampleVideo(t, strayPath)
	if err := os.Remove(strayPath); err != nil {
		t.Fatalf("removing stray temp fixture: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if got := logBuf.String(); got != "" {
		t.Errorf("pipeline logged activity for a stray temp file, want none:\n%s", got)
	}
}

func TestWatcher_StartReturnsWhenContextCancelled(t *testing.T) {
	libDir := t.TempDir()
	p, _ := newTestPipeline(t, nil)
	lib := domain.Library{
		Name:      "test-library",
		Path:      libDir,
		Languages: []language.Tag{language.English},
	}

	ready := make(chan struct{})
	w := &trigger.Watcher{
		Pipeline:   p,
		Library:    lib,
		OnWatching: func() { close(ready) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not become ready in time")
	}

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Start() error = nil, want context.Canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

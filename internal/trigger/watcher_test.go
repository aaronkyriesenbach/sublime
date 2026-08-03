package trigger_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/trigger"
)

// waitForFile polls for path to exist, failing the test if it doesn't show
// up within timeout. Watch/scan side effects land asynchronously relative to
// the test goroutine, so a fixed sleep would be both slow and flaky.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q to appear", path)
}

func TestWatcher_InitialScanProcessesPreExistingFiles(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeSampleVideo(t, videoPath)

	p := newTestPipeline(t, nil)
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

	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	waitForFile(t, sidecarPath, 2*time.Second)
}

func TestWatcher_LiveFileCreationEventIsProcessed(t *testing.T) {
	libDir := t.TempDir()

	p := newTestPipeline(t, nil)
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

	sidecarPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.en.srt")
	waitForFile(t, sidecarPath, 2*time.Second)
}

func TestWatcher_StartReturnsWhenContextCancelled(t *testing.T) {
	libDir := t.TempDir()
	p := newTestPipeline(t, nil)
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

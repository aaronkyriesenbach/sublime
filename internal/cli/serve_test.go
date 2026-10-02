package cli_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/cli"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

// lockedBuffer lets Serve's goroutines log into a buffer the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeServeTestConfig(t *testing.T, libDir string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	contents := "libraries:\n  - name: movies\n    path: " + libDir + "\n    languages: [en]\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestServe_HealthIsReachableAssoonAsListening(t *testing.T) {
	t.Setenv("SUBLIME_OPENSUBTITLES_API_KEY", "test-key")
	t.Setenv("SUBLIME_OPENSUBTITLES_USERNAME", "test-user")
	t.Setenv("SUBLIME_OPENSUBTITLES_PASSWORD", "test-pass")

	libDir := t.TempDir()
	configPath := writeServeTestConfig(t, libDir)
	dbPath := filepath.Join(t.TempDir(), "sublime.db")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan string, 1)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- cli.Serve(ctx, cli.ServeOptions{
			ConfigPath: configPath,
			DBPath:     dbPath,
			Addr:       "127.0.0.1:0",
			OnReady:    func(addr string) { ready <- addr },
			Logger:     discardLogger(),
		})
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-serveErr:
		t.Fatalf("Serve exited before becoming ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Serve to become ready")
	}

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned error after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Serve to shut down")
	}
}

func TestServe_LibrariesReflectsConfig(t *testing.T) {
	t.Setenv("SUBLIME_OPENSUBTITLES_API_KEY", "test-key")
	t.Setenv("SUBLIME_OPENSUBTITLES_USERNAME", "test-user")
	t.Setenv("SUBLIME_OPENSUBTITLES_PASSWORD", "test-pass")

	libDir := t.TempDir()
	configPath := writeServeTestConfig(t, libDir)
	dbPath := filepath.Join(t.TempDir(), "sublime.db")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan string, 1)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- cli.Serve(ctx, cli.ServeOptions{
			ConfigPath: configPath,
			DBPath:     dbPath,
			Addr:       "127.0.0.1:0",
			OnReady:    func(addr string) { ready <- addr },
			Logger:     discardLogger(),
		})
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-serveErr:
		t.Fatalf("Serve exited before becoming ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Serve to become ready")
	}
	defer func() {
		cancel()
		<-serveErr
	}()

	resp, err := http.Get("http://" + addr + "/libraries")
	if err != nil {
		t.Fatalf("GET /libraries: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServe_MissingConfigReturnsErrorImmediately(t *testing.T) {
	err := cli.Serve(context.Background(), cli.ServeOptions{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		DBPath:     filepath.Join(t.TempDir(), "sublime.db"),
		Addr:       "127.0.0.1:0",
		Logger:     discardLogger(),
	})
	if err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestServe_MissingProviderSecretsReturnsErrorImmediately(t *testing.T) {
	libDir := t.TempDir()
	configPath := writeServeTestConfig(t, libDir)

	err := cli.Serve(context.Background(), cli.ServeOptions{
		ConfigPath: configPath,
		DBPath:     filepath.Join(t.TempDir(), "sublime.db"),
		Addr:       "127.0.0.1:0",
		Logger:     discardLogger(),
	})
	if err == nil {
		t.Fatal("expected an error when OpenSubtitles secrets are missing")
	}
}

func TestServe_ResetsStaleInProgressPairsToPendingAndLogsCount(t *testing.T) {
	t.Setenv("SUBLIME_OPENSUBTITLES_API_KEY", "test-key")
	t.Setenv("SUBLIME_OPENSUBTITLES_USERNAME", "test-user")
	t.Setenv("SUBLIME_OPENSUBTITLES_PASSWORD", "test-pass")

	libDir := t.TempDir()
	configPath := writeServeTestConfig(t, libDir)
	dbPath := filepath.Join(t.TempDir(), "sublime.db")

	// A previous process claimed these pairs and died. They belong to a
	// Library the config doesn't know, so the Dispatcher leaves them Pending
	// instead of claiming them again before the test can observe them.
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening seed store: %v", err)
	}
	ctx := context.Background()
	for _, path := range []string{"/old/a.mkv", "/old/b.mkv"} {
		file, err := seed.ObserveFileContentHash(ctx, "removed-library", path, "hash")
		if err != nil {
			t.Fatalf("ObserveFileContentHash: %v", err)
		}
		if err := seed.EnsureLanguage(ctx, file.ID, language.English); err != nil {
			t.Fatalf("EnsureLanguage: %v", err)
		}
		if err := seed.MarkInProgress(ctx, file.ID, language.English); err != nil {
			t.Fatalf("MarkInProgress: %v", err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed store: %v", err)
	}

	var logs lockedBuffer
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan string, 1)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- cli.Serve(serveCtx, cli.ServeOptions{
			ConfigPath: configPath,
			DBPath:     dbPath,
			Addr:       "127.0.0.1:0",
			OnReady:    func(addr string) { ready <- addr },
			Logger:     slog.New(slog.NewTextHandler(&logs, nil)),
		})
	}()
	select {
	case <-ready:
	case err := <-serveErr:
		t.Fatalf("Serve exited before becoming ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Serve to become ready")
	}
	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("Serve returned error after shutdown: %v", err)
	}

	check, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer func() { _ = check.Close() }()
	for _, path := range []string{"/old/a.mkv", "/old/b.mkv"} {
		file, ok, err := check.GetFile(ctx, "removed-library", path)
		if err != nil || !ok {
			t.Fatalf("GetFile(%s) = ok %v, err %v", path, ok, err)
		}
		if got := file.Languages[0].Status; got != domain.StatusPending {
			t.Errorf("%s status = %q, want pending", path, got)
		}
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, "stale in-progress") || !strings.Contains(logOutput, "count=2") {
		t.Errorf("log output = %q, want one recovery line with count=2", logOutput)
	}
	if n := strings.Count(logOutput, "stale in-progress"); n != 1 {
		t.Errorf("recovery logged %d times, want once", n)
	}
}

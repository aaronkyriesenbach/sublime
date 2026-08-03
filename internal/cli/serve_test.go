package cli_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/cli"
)

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

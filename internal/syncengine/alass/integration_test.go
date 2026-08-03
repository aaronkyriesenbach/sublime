package alass_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/syncengine/alass"
)

const (
	integrationVideoDir = "../../../testdata/integration/video"
	integrationSubsDir  = "../../../testdata/integration/subs"
)

func requireRealAlass(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("alass")
	if err != nil {
		t.Skip("alass binary not found on PATH; skipping real-alass integration test (see issue #14 for the CLI contract it exercises)")
	}
	return path
}

// TestEngine_Sync_Integration_Success runs the real alass binary against
// video/sample.mp4 as the reference and subs/sample.shifted.srt (the same
// speech, timed +2.5s off) as the candidate, and checks it produces a
// re-timed subtitle rather than a passthrough copy.
func TestEngine_Sync_Integration_Success(t *testing.T) {
	binaryPath := requireRealAlass(t)

	reference := filepath.Join(integrationVideoDir, "sample.mp4")
	candidate := filepath.Join(integrationSubsDir, "sample.shifted.srt")
	outputPath := filepath.Join(t.TempDir(), "aligned.srt")

	engine := &alass.Engine{BinaryPath: binaryPath}

	got, err := engine.Sync(reference, candidate, outputPath)
	if err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if got != outputPath {
		t.Errorf("Sync returned %q, want %q", got, outputPath)
	}

	outContent, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading aligned output: %v", err)
	}
	if len(bytes.TrimSpace(outContent)) == 0 {
		t.Fatal("expected non-empty aligned subtitle output")
	}

	shiftedContent, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatalf("reading shifted candidate: %v", err)
	}
	if bytes.Equal(outContent, shiftedContent) {
		t.Error("expected alass to re-time the shifted subtitle, but output is byte-identical to the shifted input")
	}
}

// TestEngine_Sync_Integration_Failure runs the real alass binary against a
// candidate subtitle path that doesn't exist. The extension is valid, so
// Sync's own pre-flight checks pass and the subprocess actually runs,
// exercising the real exit-code-1 / stdout-error path end to end.
func TestEngine_Sync_Integration_Failure(t *testing.T) {
	binaryPath := requireRealAlass(t)

	reference := filepath.Join(integrationVideoDir, "sample.mp4")
	candidate := filepath.Join(t.TempDir(), "missing.srt")
	outputPath := filepath.Join(t.TempDir(), "aligned.srt")

	engine := &alass.Engine{BinaryPath: binaryPath}

	_, err := engine.Sync(reference, candidate, outputPath)
	if err == nil {
		t.Fatal("expected an error for a missing candidate subtitle file, got nil")
	}

	var runErr *alass.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("expected a *alass.RunError from the real alass binary, got %T: %v", err, err)
	}
	if runErr.ExitCode != 1 {
		t.Errorf("RunError.ExitCode = %d, want 1", runErr.ExitCode)
	}
	if runErr.Output == "" {
		t.Error("expected alass's stdout diagnostic to be captured in RunError.Output, got empty string")
	}
}

package alass_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine/alass"
)

const (
	fakeSuccessBin = "testdata/fakebin/success.sh"
	fakeFailBin    = "testdata/fakebin/fail.sh"
	fakeStderrOnly = "testdata/fakebin/fail_stderr_only.sh"
)

func writeFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing %q: %v", path, err)
	}
	return path
}

func TestEngine_Sync_Success(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "1\n00:00:03,000 --> 00:00:04,400\nOne two three\n")
	outputPath := filepath.Join(dir, "output.srt")
	argsFile := filepath.Join(dir, "args.txt")

	t.Setenv("FAKE_ALASS_ARGS_FILE", argsFile)
	engine := &alass.Engine{BinaryPath: fakeSuccessBin, Flags: []string{"--no-split"}}

	got, err := engine.Sync(reference, candidate, outputPath)
	if err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if got != outputPath {
		t.Errorf("Sync returned %q, want %q", got, outputPath)
	}

	gotContent, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	wantContent, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatalf("reading candidate: %v", err)
	}
	if string(gotContent) != string(wantContent) {
		t.Errorf("output content = %q, want %q", gotContent, wantContent)
	}

	argsContent, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading recorded args: %v", err)
	}
	wantArgs := reference + "\n" + candidate + "\n" + outputPath + "\n--no-split\n"
	if string(argsContent) != wantArgs {
		t.Errorf("alass invoked with args %q, want %q", argsContent, wantArgs)
	}
}

func TestEngine_Sync_Failure_IsTerminalNotRetried(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "subtitle content")
	outputPath := filepath.Join(dir, "output.srt")

	engine := &alass.Engine{BinaryPath: fakeFailBin}

	_, err := engine.Sync(reference, candidate, outputPath)
	if err == nil {
		t.Fatal("expected an error for a non-zero alass exit, got nil")
	}

	var runErr *alass.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("expected a *alass.RunError, got %T: %v", err, err)
	}
	if runErr.ExitCode != 1 {
		t.Errorf("RunError.ExitCode = %d, want 1", runErr.ExitCode)
	}
	if !strings.Contains(runErr.Output, "could not parse incorrect subtitle file") {
		t.Errorf("RunError.Output = %q, want it to contain alass's stdout diagnostic", runErr.Output)
	}

	var transient *retry.TransientError
	if errors.As(err, &transient) {
		t.Error("a non-zero alass exit must not be classified as retry.Transient — alass failures are deterministic")
	}
}

func TestEngine_Sync_Failure_IgnoresStderr(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "subtitle content")
	outputPath := filepath.Join(dir, "output.srt")

	engine := &alass.Engine{BinaryPath: fakeStderrOnly}

	_, err := engine.Sync(reference, candidate, outputPath)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), "this line must never appear") {
		t.Errorf("error message read stderr instead of stdout: %v", err)
	}
}

func TestEngine_Sync_BinaryNotFound(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "subtitle content")
	outputPath := filepath.Join(dir, "output.srt")

	engine := &alass.Engine{BinaryPath: filepath.Join(dir, "does-not-exist-alass")}

	_, err := engine.Sync(reference, candidate, outputPath)
	if err == nil {
		t.Fatal("expected an error when the alass binary doesn't exist, got nil")
	}

	var runErr *alass.RunError
	if errors.As(err, &runErr) {
		t.Errorf("expected a plain start-failure error (no exit code available), got *alass.RunError: %v", runErr)
	}
	var transient *retry.TransientError
	if errors.As(err, &transient) {
		t.Error("a missing alass binary must not be classified as retry.Transient")
	}
}

func TestEngine_Sync_UnsupportedCandidateExtension(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.txt", "subtitle content")
	outputPath := filepath.Join(dir, "output.txt")

	// BinaryPath points at a script that fails the test if ever invoked,
	// proving the extension check runs before shelling out.
	engine := &alass.Engine{BinaryPath: fakeFailBin}

	_, err := engine.Sync(reference, candidate, outputPath)
	if err == nil {
		t.Fatal("expected an error for an unsupported candidate subtitle extension, got nil")
	}
	var runErr *alass.RunError
	if errors.As(err, &runErr) {
		t.Error("expected a pre-flight validation error, not a *alass.RunError from actually running alass")
	}
}

func TestEngine_Sync_OutputExtensionMismatch(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "subtitle content")
	outputPath := filepath.Join(dir, "output.ass")

	engine := &alass.Engine{BinaryPath: fakeFailBin}

	_, err := engine.Sync(reference, candidate, outputPath)
	if err == nil {
		t.Fatal("expected an error when output extension doesn't match the candidate's, got nil")
	}
	var runErr *alass.RunError
	if errors.As(err, &runErr) {
		t.Error("expected a pre-flight validation error, not a *alass.RunError from actually running alass")
	}
}

func TestNew_ReturnsDefaultEngine(t *testing.T) {
	engine := alass.New()
	if engine.BinaryPath != "" {
		t.Errorf("New().BinaryPath = %q, want empty string (means \"alass\" on PATH)", engine.BinaryPath)
	}
	if len(engine.Flags) != 0 {
		t.Errorf("New().Flags = %v, want empty", engine.Flags)
	}
}

func TestRunError_ErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *alass.RunError
		want string
	}{
		{"with output", &alass.RunError{ExitCode: 1, Output: "error: bad input\n"}, "alass exited with status 1: error: bad input"},
		{"empty output", &alass.RunError{ExitCode: 1, Output: ""}, "alass exited with status 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// var _ ensures Engine keeps satisfying SyncEngine at compile time.
var _ syncengine.SyncEngine = (*alass.Engine)(nil)

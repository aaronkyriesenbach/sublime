package syncengine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

func writeFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing %q: %v", path, err)
	}
	return path
}

func TestFakeSyncEngine_Passthrough(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n")
	outputPath := filepath.Join(dir, "output.srt")

	engine := &syncengine.FakeSyncEngine{}

	got, err := engine.Sync(reference, candidate, outputPath)
	if err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if got != outputPath {
		t.Errorf("Sync returned %q, want %q", got, outputPath)
	}

	wantContent, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatalf("reading candidate: %v", err)
	}
	gotContent, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if string(gotContent) != string(wantContent) {
		t.Errorf("output content = %q, want passthrough of candidate %q", gotContent, wantContent)
	}
}

func TestFakeSyncEngine_FixedOutputContent(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n")
	outputPath := filepath.Join(dir, "output.srt")

	shifted := "1\n00:00:03,000 --> 00:00:04,400\nOne two three\n"
	engine := &syncengine.FakeSyncEngine{OutputContent: []byte(shifted)}

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
	if string(gotContent) != shifted {
		t.Errorf("output content = %q, want configured shift %q", gotContent, shifted)
	}
}

func TestFakeSyncEngine_ConfiguredError(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "subtitle content")
	outputPath := filepath.Join(dir, "output.srt")

	wantErr := errors.New("alignment failed")
	engine := &syncengine.FakeSyncEngine{Err: wantErr}

	got, err := engine.Sync(reference, candidate, outputPath)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Sync error = %v, want %v", err, wantErr)
	}
	if got != "" {
		t.Errorf("Sync returned output path %q on error, want empty string", got)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Errorf("expected no output file to be written on error, stat err = %v", statErr)
	}
}

func TestFakeSyncEngine_MissingCandidate(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	outputPath := filepath.Join(dir, "output.srt")

	engine := &syncengine.FakeSyncEngine{}

	_, err := engine.Sync(reference, filepath.Join(dir, "missing.srt"), outputPath)
	if err == nil {
		t.Fatal("expected an error for a missing candidate subtitle, got nil")
	}
}

func TestFakeSyncEngine_RecordsCalls(t *testing.T) {
	dir := t.TempDir()
	reference := writeFile(t, dir, "reference.mp4", "not a real video")
	candidate := writeFile(t, dir, "candidate.srt", "subtitle content")
	outputPath := filepath.Join(dir, "output.srt")

	engine := &syncengine.FakeSyncEngine{}
	if _, err := engine.Sync(reference, candidate, outputPath); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	if len(engine.Calls) != 1 {
		t.Fatalf("expected 1 recorded call, got %d", len(engine.Calls))
	}
	want := syncengine.Call{
		ReferenceVideoPath:    reference,
		CandidateSubtitlePath: candidate,
		OutputPath:            outputPath,
	}
	if engine.Calls[0] != want {
		t.Errorf("recorded call = %+v, want %+v", engine.Calls[0], want)
	}
}

// var _ ensures FakeSyncEngine keeps satisfying SyncEngine at compile time.
var _ syncengine.SyncEngine = (*syncengine.FakeSyncEngine)(nil)

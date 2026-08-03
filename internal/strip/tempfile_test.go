package strip_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

func TestIsStrayTempFile_RecognizesGeneratedTempNames(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Movie.sublime-strip-tmp-a1b2c3d4.mkv", true},
		{"Movie.en.sublime-strip-tmp-deadbeef.srt", true},
		{"Movie.mkv", false},
		{"Movie.en.srt", false},
		{"sublime-strip-tmp", false}, // missing the required "-" + suffix
	}

	for _, tc := range cases {
		if got := strip.IsStrayTempFile(tc.name); got != tc.want {
			t.Errorf("IsStrayTempFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewTempVideoPath_IsRecognizedAsStray(t *testing.T) {
	path := strip.NewTempVideoPath("/media/movies", "Movie", ".mkv")
	if dir := filepath.Dir(path); dir != "/media/movies" {
		t.Errorf("expected temp path in the same directory, got %q", dir)
	}
	if !strip.IsStrayTempFile(filepath.Base(path)) {
		t.Errorf("NewTempVideoPath result %q not recognized as a stray temp file", path)
	}
	if filepath.Ext(path) != ".mkv" {
		t.Errorf("expected temp path to preserve the original extension, got %q", path)
	}
}

func TestNewTempSidecarPath_IsRecognizedAsStray(t *testing.T) {
	path := strip.NewTempSidecarPath("/media/movies", "Movie", ".srt")
	if dir := filepath.Dir(path); dir != "/media/movies" {
		t.Errorf("expected temp path in the same directory, got %q", dir)
	}
	if !strip.IsStrayTempFile(filepath.Base(path)) {
		t.Errorf("NewTempSidecarPath result %q not recognized as a stray temp file", path)
	}
}

func TestNewTempVideoPath_IsUniquePerCall(t *testing.T) {
	a := strip.NewTempVideoPath("/media/movies", "Movie", ".mkv")
	b := strip.NewTempVideoPath("/media/movies", "Movie", ".mkv")
	if a == b {
		t.Errorf("expected two calls to generate distinct temp paths, both were %q", a)
	}
}

func TestCleanStrayTempFiles_RemovesOnlyStrayTempFilesRecursively(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	keep := []string{
		filepath.Join(root, "Movie.mkv"),
		filepath.Join(root, "Movie.en.srt"),
		filepath.Join(sub, "Episode.mkv"),
	}
	stray := []string{
		filepath.Join(root, "Movie.sublime-strip-tmp-aaaaaaaa.mkv"),
		filepath.Join(sub, "Episode.en.sublime-strip-tmp-bbbbbbbb.srt"),
	}

	for _, p := range append(append([]string{}, keep...), stray...) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing fixture file %q: %v", p, err)
		}
	}

	removed, err := strip.CleanStrayTempFiles(root)
	if err != nil {
		t.Fatalf("CleanStrayTempFiles returned error: %v", err)
	}
	if removed != len(stray) {
		t.Errorf("expected %d files removed, got %d", len(stray), removed)
	}

	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q to survive cleanup, stat error: %v", p, err)
		}
	}
	for _, p := range stray {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %q to be removed, stat error: %v", p, err)
		}
	}
}

func TestCleanStrayTempFiles_EmptyDirectory(t *testing.T) {
	removed, err := strip.CleanStrayTempFiles(t.TempDir())
	if err != nil {
		t.Fatalf("CleanStrayTempFiles returned error: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed in an empty directory, got %d", removed)
	}
}

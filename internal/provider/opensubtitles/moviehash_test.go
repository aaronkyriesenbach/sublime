package opensubtitles_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/provider/opensubtitles"
)

// pattern generates a deterministic n-byte sequence (byte i = i mod 256),
// reproduced independently in Python to compute this file's oracle hash
// values below (see the package's dev notes) rather than by calling the
// code under test.
func pattern(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i)
	}
	return data
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mkv")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	return path
}

func TestComputeMovieHash_SmallFileOverlap(t *testing.T) {
	// 16 bytes: well under the 64 KiB window, so head and tail overlap the
	// entire file, mirroring Sublime's own Content Hash tiny-file handling.
	path := writeFile(t, pattern(16))

	got, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}
	if want := "2c2824201c181420"; got != want {
		t.Errorf("ComputeMovieHash() = %q, want %q", got, want)
	}
}

func TestComputeMovieHash_OddSizeDropsPartialTrailingWord(t *testing.T) {
	// 20 bytes: not a multiple of 8, so the final partial word is dropped
	// from the sum, matching the reference algorithm's fixed word-count
	// loop.
	path := writeFile(t, pattern(20))

	got, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}
	if want := "2c2824201c181424"; got != want {
		t.Errorf("ComputeMovieHash() = %q, want %q", got, want)
	}
}

func TestComputeMovieHash_LargeFileNoOverlap(t *testing.T) {
	// 140000 bytes: comfortably over 2x the 64 KiB window, so head and tail
	// cover disjoint regions with a gap between them.
	path := writeFile(t, pattern(140000))

	got, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}
	if want := "a0601fdf9f6122e0"; got != want {
		t.Errorf("ComputeMovieHash() = %q, want %q", got, want)
	}
}

func TestComputeMovieHash_ExactBoundaryNoGapNoOverlap(t *testing.T) {
	// 131072 bytes (2x 64 KiB) exactly: head and tail are adjacent, with
	// neither a gap nor an overlap between them.
	path := writeFile(t, pattern(131072))

	got, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}
	if want := "a0601fdf9f610000"; got != want {
		t.Errorf("ComputeMovieHash() = %q, want %q", got, want)
	}
}

func TestComputeMovieHash_EmptyFile(t *testing.T) {
	path := writeFile(t, nil)

	got, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}
	if want := "0000000000000000"; got != want {
		t.Errorf("ComputeMovieHash() = %q, want %q", got, want)
	}
}

func TestComputeMovieHash_Stable(t *testing.T) {
	path := writeFile(t, pattern(200000))

	first, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}
	second, err := opensubtitles.ComputeMovieHash(path)
	if err != nil {
		t.Fatalf("ComputeMovieHash() second call error = %v", err)
	}
	if first != second {
		t.Errorf("ComputeMovieHash() is not stable: %q != %q", first, second)
	}
}

func TestComputeMovieHash_MissingFile(t *testing.T) {
	if _, err := opensubtitles.ComputeMovieHash(filepath.Join(t.TempDir(), "missing.mkv")); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

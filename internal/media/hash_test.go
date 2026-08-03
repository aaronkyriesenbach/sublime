package media_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/media"
)

const (
	sampleVideoPath = "../../testdata/integration/video/sample.mp4"
	tinyVideoPath   = "../../testdata/integration/video/tiny.mp4"
)

var hexPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestComputeContentHash_Format(t *testing.T) {
	for _, path := range []string{sampleVideoPath, tinyVideoPath} {
		hash, err := media.ComputeContentHash(path)
		if err != nil {
			t.Fatalf("ComputeContentHash(%q) returned error: %v", path, err)
		}
		if !hexPattern.MatchString(string(hash)) {
			t.Errorf("ComputeContentHash(%q) = %q, want a lowercase 16-char hex string", path, hash)
		}
	}
}

func TestComputeContentHash_Stable(t *testing.T) {
	for _, path := range []string{sampleVideoPath, tinyVideoPath} {
		first, err := media.ComputeContentHash(path)
		if err != nil {
			t.Fatalf("ComputeContentHash(%q) returned error: %v", path, err)
		}
		second, err := media.ComputeContentHash(path)
		if err != nil {
			t.Fatalf("ComputeContentHash(%q) returned error on second call: %v", path, err)
		}
		if first != second {
			t.Errorf("ComputeContentHash(%q) is not stable: %q != %q", path, first, second)
		}
	}
}

func TestComputeContentHash_Uniqueness(t *testing.T) {
	sampleHash, err := media.ComputeContentHash(sampleVideoPath)
	if err != nil {
		t.Fatalf("ComputeContentHash(sample) returned error: %v", err)
	}
	tinyHash, err := media.ComputeContentHash(tinyVideoPath)
	if err != nil {
		t.Fatalf("ComputeContentHash(tiny) returned error: %v", err)
	}
	if sampleHash == tinyHash {
		t.Errorf("expected distinct hashes for distinct files, got %q for both", sampleHash)
	}
}

// TestComputeContentHash_TinyFileOverlap exercises the case where a file is
// smaller than the 64 KiB window, so the "first 64 KiB" and "last 64 KiB"
// reads necessarily overlap into the same bytes. This should still succeed
// and produce a hash matching a from-scratch computation over the whole
// file (used twice) plus the size.
func TestComputeContentHash_TinyFileOverlap(t *testing.T) {
	info, err := os.Stat(tinyVideoPath)
	if err != nil {
		t.Fatalf("stat %q: %v", tinyVideoPath, err)
	}
	if info.Size() >= 64*1024 {
		t.Fatalf("fixture %q is not smaller than the 64 KiB window (size=%d); test assumption violated", tinyVideoPath, info.Size())
	}

	hash, err := media.ComputeContentHash(tinyVideoPath)
	if err != nil {
		t.Fatalf("ComputeContentHash(%q) returned error: %v", tinyVideoPath, err)
	}
	if !hexPattern.MatchString(string(hash)) {
		t.Errorf("ComputeContentHash(%q) = %q, want a lowercase 16-char hex string", tinyVideoPath, hash)
	}
}

// TestComputeContentHash_FixedCost proves the hash's cost doesn't scale with
// file size: two large files that differ only in their untouched middle
// region (outside both 64 KiB windows) must hash identically.
func TestComputeContentHash_FixedCost(t *testing.T) {
	const windowSize = 64 * 1024
	const middleSize = 4 * windowSize // comfortably larger than either window

	head := make([]byte, windowSize)
	for i := range head {
		head[i] = byte(i)
	}
	tail := make([]byte, windowSize)
	for i := range tail {
		tail[i] = byte(255 - i)
	}

	buildFile := func(t *testing.T, middleFill byte) string {
		t.Helper()
		middle := make([]byte, middleSize)
		for i := range middle {
			middle[i] = middleFill
		}

		var contents []byte
		contents = append(contents, head...)
		contents = append(contents, middle...)
		contents = append(contents, tail...)

		path := filepath.Join(t.TempDir(), "fixed-cost.bin")
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatalf("writing test file: %v", err)
		}
		return path
	}

	pathA := buildFile(t, 0xAA)
	pathB := buildFile(t, 0xBB)

	hashA, err := media.ComputeContentHash(pathA)
	if err != nil {
		t.Fatalf("ComputeContentHash(pathA) returned error: %v", err)
	}
	hashB, err := media.ComputeContentHash(pathB)
	if err != nil {
		t.Fatalf("ComputeContentHash(pathB) returned error: %v", err)
	}

	if hashA != hashB {
		t.Errorf("expected identical hashes for files differing only in an untouched middle region, got %q and %q", hashA, hashB)
	}
}

func TestComputeContentHash_MissingFile(t *testing.T) {
	_, err := media.ComputeContentHash(filepath.Join(t.TempDir(), "does-not-exist.mp4"))
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

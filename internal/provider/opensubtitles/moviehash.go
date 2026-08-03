package opensubtitles

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// movieHashWindow is the number of bytes read from the start and end of
// the file, per OpenSubtitles' own legacy moviehash algorithm.
const movieHashWindow = 64 * 1024

// ComputeMovieHash computes OpenSubtitles' own legacy "moviehash" search
// parameter for the video file at path: the file size plus the sum of the
// first and last 64 KiB's 8-byte little-endian words, wrapping as an
// unsigned 64-bit integer, encoded as lowercase 16-char hex.
//
// This is OpenSubtitles' own hash, computed independently from Sublime's
// Content Hash (internal/media) — the two share only the fixed-cost
// head+tail+size *shape*, never the algorithm or a code path; see
// CONTEXT.md's Content Hash entry and issue #13's research notes. For
// files smaller than the 128 KiB the reference algorithm assumes (2x the
// window), the head and tail reads overlap the same bytes rather than
// erroring, mirroring Content Hash's own tiny-file handling.
func ComputeMovieHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opensubtitles: opening %q: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("opensubtitles: stat %q: %w", path, err)
	}
	size := info.Size()

	hash := uint64(size)

	headSum, err := sumWords(f, 0, size)
	if err != nil {
		return "", fmt.Errorf("opensubtitles: hashing head of %q: %w", path, err)
	}
	hash += headSum

	tailOffset := size - min(int64(movieHashWindow), size)
	tailSum, err := sumWords(f, tailOffset, size)
	if err != nil {
		return "", fmt.Errorf("opensubtitles: hashing tail of %q: %w", path, err)
	}
	hash += tailSum

	return fmt.Sprintf("%016x", hash), nil
}

// sumWords reads up to movieHashWindow bytes starting at offset (clamped to
// size) and returns the unsigned sum of its 8-byte little-endian words. A
// final partial word (fewer than 8 bytes) is dropped, matching the
// reference algorithm's fixed word-count loop. Note that for files smaller
// than movieHashWindow, the head call (offset 0) and the tail call (offset
// also 0, since size-min(window,size) is 0) read the identical bytes —
// this is what produces the tiny-file overlap described above, without any
// separate branch for it.
func sumWords(f *os.File, offset, size int64) (uint64, error) {
	n := min(int64(movieHashWindow), size-offset)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return 0, err
	}

	var sum uint64
	for i := 0; i+8 <= len(buf); i += 8 {
		sum += binary.LittleEndian.Uint64(buf[i : i+8])
	}
	return sum, nil
}

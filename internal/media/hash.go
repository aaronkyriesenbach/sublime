// Package media computes a video file's Content Hash and classifies it as a
// movie or TV episode. See CONTEXT.md at the repo root for the Content Hash
// definition these functions implement.
package media

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/cespare/xxhash/v2"
)

// hashWindow is the number of bytes read from the start and end of the file.
const hashWindow = 64 * 1024

// ContentHash is Sublime's fixed-cost identity hash for a video file's
// content: XXH64 over the first 64 KiB, the last 64 KiB, and the file's
// 8-byte little-endian size, encoded as lowercase 16-char hex. It is
// independent of the file's total size, unlike a full-content checksum.
type ContentHash string

// ComputeContentHash computes the Content Hash of the video file at path.
// For files smaller than the 64 KiB window, the head and tail reads overlap
// (both cover the entire file); this is intentional and still deterministic.
func ComputeContentHash(path string) (ContentHash, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %q: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	size := info.Size()

	h := xxhash.New()

	headLen := min(int64(hashWindow), size)
	head := make([]byte, headLen)
	if _, err := io.ReadFull(f, head); err != nil {
		return "", fmt.Errorf("reading head of %q: %w", path, err)
	}
	if _, err := h.Write(head); err != nil {
		return "", fmt.Errorf("hashing head of %q: %w", path, err)
	}

	tailLen := min(int64(hashWindow), size)
	if _, err := f.Seek(size-tailLen, io.SeekStart); err != nil {
		return "", fmt.Errorf("seeking tail of %q: %w", path, err)
	}
	tail := make([]byte, tailLen)
	if _, err := io.ReadFull(f, tail); err != nil {
		return "", fmt.Errorf("reading tail of %q: %w", path, err)
	}
	if _, err := h.Write(tail); err != nil {
		return "", fmt.Errorf("hashing tail of %q: %w", path, err)
	}

	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(size))
	if _, err := h.Write(sizeBuf[:]); err != nil {
		return "", fmt.Errorf("hashing size of %q: %w", path, err)
	}

	return ContentHash(fmt.Sprintf("%016x", h.Sum64())), nil
}

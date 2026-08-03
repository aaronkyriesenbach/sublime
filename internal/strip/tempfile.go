package strip

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tempMarker is the fixed, unambiguous substring every Strip-generated temp
// file name contains — both the embedded-stream remux temp and the sidecar
// write temp. IsStrayTempFile and CleanStrayTempFiles key off this same
// substring, so a crash between "write temp" and "rename into place" always
// leaves a file recognizable as Sublime's own leftover, not a foreign one.
const tempMarker = "sublime-strip-tmp"

// IsStrayTempFile reports whether name (a base file name, not a full path)
// matches Strip's temp-file naming convention.
func IsStrayTempFile(name string) bool {
	return strings.Contains(name, tempMarker+"-")
}

// NewTempVideoPath returns a fresh, unique sibling temp path for remuxing
// the video at dir/stem<ext>, preserving ext so ffmpeg's format-from-extension
// inference still targets the right container.
func NewTempVideoPath(dir, stem, ext string) string {
	return newTempPath(dir, stem, ext)
}

// NewTempSidecarPath returns a fresh, unique sibling temp path for writing a
// new sidecar at dir/stem<ext> before it's atomically renamed into place.
func NewTempSidecarPath(dir, stem, ext string) string {
	return newTempPath(dir, stem, ext)
}

func newTempPath(dir, stem, ext string) string {
	return filepath.Join(dir, fmt.Sprintf("%s.%s-%s%s", stem, tempMarker, randHex(8), ext))
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read on the standard reader only fails if the OS
		// entropy source itself is broken — nothing a caller could act on,
		// and a fixed fallback still keeps the name unique enough within a
		// single process run given the surrounding stem/ext.
		return "fallback"
	}
	return hex.EncodeToString(buf)
}

// CleanStrayTempFiles removes every stray Strip temp file found anywhere
// under root, recursing into subdirectories. Intended to be called on
// startup and at the start of each Library scan, so a temp file orphaned by
// a crash between "write" and "rename" doesn't silently accumulate disk
// usage over a long-running daemon's life.
func CleanStrayTempFiles(root string) (int, error) {
	removed := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !IsStrayTempFile(d.Name()) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("strip: cleaning stray temp files under %q: %w", root, err)
	}
	return removed, nil
}

// renameOver renames tmp to final, cleaning up tmp if the rename itself
// fails so a failed swap never leaves a stray temp file for
// CleanStrayTempFiles to find later.
func renameOver(tmp, final string) error {
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("strip: renaming %q to %q: %w", tmp, final, err)
	}
	return nil
}

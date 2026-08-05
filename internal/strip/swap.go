package strip

import (
	"context"
	"path/filepath"
	"strings"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// SwapResult reports what a Swap call actually did: the new sidecar's
// final path, and everything Strip removed to make room for it.
type SwapResult struct {
	// SidecarPath is the final path of the newly written sidecar.
	SidecarPath string

	// RemovedSidecars lists every foreign/stale sidecar Strip deleted.
	RemovedSidecars []string
}

// Swap performs Sublime's fetch-then-swap: sidecarContent must already be a
// fully Synced, Marker-embedded replacement (Swap has no opinion on its
// contents) — callers must never call Swap before that replacement exists,
// so a failed search/sync always leaves the video exactly as it was.
//
// Swap only writes the new sidecar and cleans up stale/foreign ones — it
// does not touch embedded subtitle streams; callers needing that call
// Stripper.StripEmbedded themselves before Swap (see CONTEXT.md's Content
// Hash entry for why that ordering matters: StripEmbedded mutates the
// video, so the Content Hash bound into sidecarContent must already
// reflect its post-Strip state).
//
// Order of operations, matching CONTEXT.md's Strip entry:
//  1. The new sidecar is written to its final path via a sibling temp file
//     + atomic rename.
//  2. Only then are old, untrusted sidecars matching scope removed — after
//     the new one is already in place, so there's never a moment with zero
//     subtitles for lang.
func (s *FFStripper) Swap(
	_ context.Context,
	videoPath string,
	lang language.Tag,
	scope domain.StripScope,
	hash media.ContentHash,
	sidecarExt string,
	sidecarContent []byte,
) (SwapResult, error) {
	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	sidecarPath, err := WriteSidecar(dir, stem, lang, sidecarExt, sidecarContent)
	if err != nil {
		return SwapResult{}, err
	}

	removedSidecars, err := StripSidecars(dir, stem, scope, lang, hash, sidecarPath)
	if err != nil {
		return SwapResult{SidecarPath: sidecarPath}, err
	}

	return SwapResult{
		SidecarPath:     sidecarPath,
		RemovedSidecars: removedSidecars,
	}, nil
}

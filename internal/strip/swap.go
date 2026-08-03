package strip

import (
	"context"
	"fmt"
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

	// RemovedEmbeddedStreams lists the absolute container indices of every
	// embedded subtitle stream Strip removed from the video.
	RemovedEmbeddedStreams []int
}

// Swap performs Sublime's fetch-then-swap: sidecarContent must already be a
// fully Synced, Marker-embedded replacement (Swap has no opinion on its
// contents) — callers must never call Swap before that replacement exists,
// so a failed search/sync always leaves the video exactly as it was.
//
// Order of operations, matching CONTEXT.md's Strip entry:
//  1. Embedded subtitle streams matching scope are remuxed out first (via a
//     sibling temp file + atomic rename) — independent of the sidecar, and
//     skipped entirely if there's nothing to remove.
//  2. The new sidecar is written to its final path via a sibling temp file
//     + atomic rename.
//  3. Only then are old, untrusted sidecars matching scope removed — after
//     the new one is already in place, so there's never a moment with zero
//     subtitles for lang.
func (s *FFStripper) Swap(
	ctx context.Context,
	videoPath string,
	lang language.Tag,
	scope domain.StripScope,
	hash media.ContentHash,
	sidecarExt string,
	sidecarContent []byte,
) (SwapResult, error) {
	removedStreams, err := s.StripEmbedded(ctx, videoPath, scope, lang)
	if err != nil {
		return SwapResult{}, fmt.Errorf("strip: stripping embedded streams for %q: %w", videoPath, err)
	}

	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	sidecarPath, err := WriteSidecar(dir, stem, lang, sidecarExt, sidecarContent)
	if err != nil {
		return SwapResult{RemovedEmbeddedStreams: removedStreams}, err
	}

	removedSidecars, err := StripSidecars(dir, stem, scope, lang, hash, sidecarPath)
	if err != nil {
		return SwapResult{
			SidecarPath:            sidecarPath,
			RemovedEmbeddedStreams: removedStreams,
		}, err
	}

	return SwapResult{
		SidecarPath:            sidecarPath,
		RemovedSidecars:        removedSidecars,
		RemovedEmbeddedStreams: removedStreams,
	}, nil
}

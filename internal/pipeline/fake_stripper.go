package pipeline

import (
	"context"
	"path/filepath"
	"strings"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

// FakeStripper is a test double for Stripper that records calls and writes
// sidecars without shelling out to ffprobe/ffmpeg.
type FakeStripper struct {
	// Calls records every Swap invocation.
	Calls []FakeStripperCall

	// Err, if set, is returned by Swap instead of writing the sidecar.
	Err error
}

// FakeStripperCall records a single Swap invocation.
type FakeStripperCall struct {
	VideoPath      string
	Lang           language.Tag
	Scope          domain.StripScope
	Hash           media.ContentHash
	SidecarExt     string
	SidecarContent []byte
}

var _ Stripper = (*FakeStripper)(nil)

// Swap implements Stripper by recording the call and writing the sidecar
// using strip.WriteSidecar (no embedded stream handling).
func (f *FakeStripper) Swap(
	ctx context.Context,
	videoPath string,
	lang language.Tag,
	scope domain.StripScope,
	hash media.ContentHash,
	sidecarExt string,
	sidecarContent []byte,
) (strip.SwapResult, error) {
	f.Calls = append(f.Calls, FakeStripperCall{
		VideoPath:      videoPath,
		Lang:           lang,
		Scope:          scope,
		Hash:           hash,
		SidecarExt:     sidecarExt,
		SidecarContent: sidecarContent,
	})

	if f.Err != nil {
		return strip.SwapResult{}, f.Err
	}

	dir := filepath.Dir(videoPath)
	base := filepath.Base(videoPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	sidecarPath, err := strip.WriteSidecar(dir, stem, lang, sidecarExt, sidecarContent)
	if err != nil {
		return strip.SwapResult{}, err
	}

	return strip.SwapResult{SidecarPath: sidecarPath}, nil
}

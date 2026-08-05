package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

// FakeStripper is a test double for Stripper that records calls and writes
// sidecars without shelling out to ffprobe/ffmpeg. Safe for concurrent use
// by multiple pipeline workers, since a Library run may process several
// (file, language) pairs, including different languages of the same file,
// in parallel.
type FakeStripper struct {
	mu sync.Mutex

	// Calls records every Swap invocation.
	Calls []FakeStripperCall

	// Err, if set, is returned by Swap instead of writing the sidecar.
	Err error

	// StripEmbeddedCalls records every StripEmbedded invocation.
	StripEmbeddedCalls []FakeStripEmbeddedCall

	// StripEmbeddedIndices, when non-empty, is returned by StripEmbedded as
	// the removed stream indices, simulating a Strip pass that found
	// something to remove. StripEmbedded also appends
	// StripEmbeddedMutation to videoPath's on-disk bytes in that case — the
	// same way the real ffmpeg remux would mutate the file — so a
	// subsequent media.ComputeContentHash genuinely differs. Left nil (the
	// default), StripEmbedded reports nothing removed and leaves the video
	// untouched.
	StripEmbeddedIndices []int

	// StripEmbeddedMutation is appended to videoPath's content when
	// StripEmbeddedIndices is non-empty. Defaults to a fixed marker byte
	// sequence if nil.
	StripEmbeddedMutation []byte

	// StripEmbeddedErr, if set, is returned by StripEmbedded instead of
	// StripEmbeddedIndices.
	StripEmbeddedErr error
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

// FakeStripEmbeddedCall records a single StripEmbedded invocation.
type FakeStripEmbeddedCall struct {
	VideoPath string
	Scope     domain.StripScope
	Lang      language.Tag
}

var _ Stripper = (*FakeStripper)(nil)

// StripEmbedded implements Stripper by recording the call and, when
// configured via StripEmbeddedIndices, simulating a real Strip pass that
// removed something: it mutates videoPath's bytes and reports the
// configured indices.
func (f *FakeStripper) StripEmbedded(_ context.Context, videoPath string, scope domain.StripScope, lang language.Tag) ([]int, error) {
	f.mu.Lock()
	f.StripEmbeddedCalls = append(f.StripEmbeddedCalls, FakeStripEmbeddedCall{
		VideoPath: videoPath,
		Scope:     scope,
		Lang:      lang,
	})
	f.mu.Unlock()

	if f.StripEmbeddedErr != nil {
		return nil, f.StripEmbeddedErr
	}
	if len(f.StripEmbeddedIndices) == 0 {
		return nil, nil
	}

	mutation := f.StripEmbeddedMutation
	if mutation == nil {
		mutation = []byte("\x00fake-stripped-embedded-stream")
	}

	existing, err := os.ReadFile(videoPath)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(videoPath, append(existing, mutation...), 0o644); err != nil {
		return nil, err
	}

	return f.StripEmbeddedIndices, nil
}

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
	f.mu.Lock()
	f.Calls = append(f.Calls, FakeStripperCall{
		VideoPath:      videoPath,
		Lang:           lang,
		Scope:          scope,
		Hash:           hash,
		SidecarExt:     sidecarExt,
		SidecarContent: sidecarContent,
	})
	f.mu.Unlock()

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

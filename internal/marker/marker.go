// Package marker implements Sublime's Marker mechanism (see CONTEXT.md): a
// machine-readable signature embedded inside a subtitle sidecar Sublime
// produced, binding it to the Content Hash of the video it was synced
// against. Recognizing this signature on re-scan lets Sublime tell its own
// output apart from a foreign subtitle without trusting its database as
// the sole source of truth.
package marker

import (
	"fmt"
	"strings"

	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// Prefix is the literal token every Marker embeds, followed by a colon and
// the bound Content Hash. It is versioned (V1) so a future marker format
// can be introduced without misreading, or being misread as, this one.
const Prefix = "SUBLIME-MARKER-V1"

// Marker is a Sublime signature embedded in a subtitle sidecar, binding it
// to the Content Hash of the video it was synced against.
type Marker struct {
	ContentHash media.ContentHash
}

// Matches reports whether m was produced for the given Content Hash. A
// false result means the video has changed since the sidecar was written.
func (m Marker) Matches(hash media.ContentHash) bool {
	return m.ContentHash == hash
}

// Presence describes the outcome of reading a Marker back out of sidecar
// content.
type Presence int

const (
	// Absent means the sidecar carries no Marker at all — Sublime never
	// produced it, or its Marker was stripped.
	Absent Presence = iota

	// Corrupt means a Marker line was found but its hash couldn't be
	// parsed (e.g. truncated or malformed). Callers should distrust the
	// sidecar the same as Absent, but the distinction is kept for
	// diagnostics.
	Corrupt

	// Present means a well-formed Marker was found; Read's returned
	// Marker is valid.
	Present
)

// MarkerCodec writes a Marker into, and reads one back out of, a specific
// subtitle sidecar format's raw content (e.g. .srt). Each sidecar format
// Sublime supports registers its own MarkerCodec; see RegisterCodec and
// CodecFor.
type MarkerCodec interface {
	// Write returns content with a Marker for m appended, normalized to
	// UTF-8. Pre-existing cues are preserved unchanged.
	Write(content []byte, m Marker) ([]byte, error)

	// Read scans content for a previously-written Marker. The returned
	// Marker is only valid when presence is Present.
	Read(content []byte) (found Marker, presence Presence)
}

// codecs holds every registered MarkerCodec, keyed by lowercase extension
// including the leading dot (e.g. ".srt").
var codecs = map[string]MarkerCodec{}

func init() {
	RegisterCodec(".srt", srtCodec{})
}

// RegisterCodec registers codec as the MarkerCodec for sidecar files with
// the given extension (matched case-insensitively). Registering the same
// extension twice panics — that's always a startup wiring mistake, not a
// runtime condition callers should recover from.
func RegisterCodec(ext string, codec MarkerCodec) {
	key := normalizeExt(ext)
	if _, exists := codecs[key]; exists {
		panic(fmt.Sprintf("marker: codec already registered for %q", ext))
	}
	codecs[key] = codec
}

// CodecFor returns the registered MarkerCodec for ext (e.g. ".srt"), or
// false if no codec is registered for that extension.
func CodecFor(ext string) (MarkerCodec, bool) {
	codec, ok := codecs[normalizeExt(ext)]
	return codec, ok
}

func normalizeExt(ext string) string {
	return strings.ToLower(ext)
}

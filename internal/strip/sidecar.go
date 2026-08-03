package strip

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// recognizedSidecarExtensions are the sidecar file extensions Strip
// considers when looking for a video's existing subtitles. VobSub
// (.idx/.sub pairs) and PGS (.sup) sidecars are deliberately excluded — see
// CONTEXT.md's Strip entry and the "Not yet specified" note in the wayfinder
// map this ticket implements.
var recognizedSidecarExtensions = map[string]bool{
	".srt": true,
	".ass": true,
	".ssa": true,
	".vtt": true,
	".sub": true,
}

// Sidecar is one recognized subtitle sidecar file found next to a video.
type Sidecar struct {
	// Path is the sidecar's full file path.
	Path string

	// HasLanguage reports whether Language was parsed from the file name
	// (e.g. "Movie.en.srt"). An untagged sidecar (e.g. "Movie.srt") leaves
	// this false and Language zero.
	HasLanguage bool
	Language    language.Tag
}

// ListSidecars finds every recognized subtitle sidecar in dir sharing
// videoStem (a video's base name with its own extension stripped), with or
// without a BCP-47 language tag.
func ListSidecars(dir, videoStem string) ([]Sidecar, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("strip: listing sidecars in %q: %w", dir, err)
	}

	prefix := videoStem + "."
	var sidecars []Sidecar
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		rest := strings.TrimPrefix(name, prefix)
		lang, hasLang, _, ok := parseSidecarSuffix(rest)
		if !ok {
			continue
		}

		sidecars = append(sidecars, Sidecar{
			Path:        filepath.Join(dir, name),
			HasLanguage: hasLang,
			Language:    lang,
		})
	}

	return sidecars, nil
}

// parseSidecarSuffix splits rest (a sidecar file name with the shared video
// stem and its trailing "." already removed) into an optional BCP-47
// language segment and a recognized extension. ok is false if the trailing
// segment isn't a recognized sidecar extension at all.
func parseSidecarSuffix(rest string) (lang language.Tag, hasLang bool, ext string, ok bool) {
	parts := strings.Split(rest, ".")
	ext = "." + parts[len(parts)-1]
	if !recognizedSidecarExtensions[strings.ToLower(ext)] {
		return language.Tag{}, false, "", false
	}

	if len(parts) < 2 {
		return language.Tag{}, false, ext, true
	}

	tag, err := language.Parse(parts[len(parts)-2])
	if err != nil {
		return language.Tag{}, false, ext, true
	}
	return tag, true, ext, true
}

// IsSidecarTrusted reports whether the sidecar at path carries a Sublime
// Marker bound to hash — i.e. Sublime produced it for the video's current
// content, and Strip must leave it alone. Any other outcome (no registered
// MarkerCodec for its extension, no Marker found, a corrupt Marker, or a
// Marker bound to a different Content Hash) is untrusted.
func IsSidecarTrusted(path string, hash media.ContentHash) (bool, error) {
	codec, ok := marker.CodecFor(filepath.Ext(path))
	if !ok {
		return false, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("strip: reading sidecar %q: %w", path, err)
	}

	found, presence := codec.Read(content)
	if presence != marker.Present {
		return false, nil
	}
	return found.Matches(hash), nil
}

// WriteSidecar atomically writes content as the sidecar for lang at
// dir/videoStem.<lang><ext>: it writes to a sibling temp file first, then
// renames it into place, so a reader never observes a partially-written
// sidecar. Returns the final path.
func WriteSidecar(dir, videoStem string, lang language.Tag, ext string, content []byte) (string, error) {
	final := filepath.Join(dir, fmt.Sprintf("%s.%s%s", videoStem, lang.String(), ext))
	tmp := NewTempSidecarPath(dir, videoStem, ext)

	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return "", fmt.Errorf("strip: writing temp sidecar %q: %w", tmp, err)
	}
	if err := renameOver(tmp, final); err != nil {
		return "", err
	}

	return final, nil
}

// StripSidecars removes every untrusted, scope-matching sidecar found for
// videoStem in dir, per IsSidecarTrusted's Marker check. keep (typically
// the sidecar WriteSidecar just wrote) is never considered for removal,
// even if it happens to match scope/language — this is what lets a caller
// write the new sidecar into place before unlinking the old one without a
// race against its own output.
//
// In domain.StripScopePerLanguage, only sidecars whose file name carries a
// BCP-47 tag exactly matching lang are candidates; an untagged sidecar or
// one tagged for a different language is left untouched (fails closed,
// consistent with the language-matching rule described in CONTEXT.md).
func StripSidecars(dir, videoStem string, scope domain.StripScope, lang language.Tag, hash media.ContentHash, keep string) ([]string, error) {
	sidecars, err := ListSidecars(dir, videoStem)
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, sc := range sidecars {
		if sc.Path == keep {
			continue
		}
		if scope == domain.StripScopePerLanguage && (!sc.HasLanguage || sc.Language.String() != lang.String()) {
			continue
		}

		trusted, err := IsSidecarTrusted(sc.Path, hash)
		if err != nil {
			return removed, err
		}
		if trusted {
			continue
		}

		if err := os.Remove(sc.Path); err != nil {
			return removed, fmt.Errorf("strip: removing untrusted sidecar %q: %w", sc.Path, err)
		}
		removed = append(removed, sc.Path)
	}

	return removed, nil
}

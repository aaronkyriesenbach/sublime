package strip

import (
	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// NeedsFetch implements Strip's Marker+Content-Hash gate: it reports
// whether a (file, language) pair still needs a fetch, or whether an
// existing sidecar already carries a Sublime Marker bound to hash (the
// video's current Content Hash) for lang specifically.
//
// The match is a strict BCP-47 tag match against lang — an untagged
// sidecar or one tagged for a different language never satisfies the gate,
// consistent with Strip's fail-closed language matching elsewhere. When
// satisfied, the caller should skip both fetching and Strip entirely for
// this (file, language): the sidecar is Sublime's own valid prior output.
func NeedsFetch(dir, videoStem string, lang language.Tag, hash media.ContentHash) (bool, error) {
	sidecars, err := ListSidecars(dir, videoStem)
	if err != nil {
		return false, err
	}

	for _, sc := range sidecars {
		if !sc.HasLanguage || sc.Language.String() != lang.String() {
			continue
		}

		trusted, err := IsSidecarTrusted(sc.Path, hash)
		if err != nil {
			return false, err
		}
		if trusted {
			return false, nil
		}
	}

	return true, nil
}

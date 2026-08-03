// Package scoring picks the single best domain.Candidate for a video, or
// correctly picks none, per CONTEXT.md's Candidate definition: a hash match
// short-circuits scoring entirely, and absent one, Candidates are scored
// against the video's own filename-derived metadata using fixed v1 weights,
// then filtered by a cutoff built from identity attributes alone. See
// CONTEXT.md at the repo root for domain vocabulary.
package scoring

import "github.com/aaronkyriesenbach/sublime/internal/media"

// Weights are the fixed v1 lexicographic-dominance constants: each
// attribute outweighs every attribute after it combined (e.g. title's 64
// beats year+season/episode+source+releaseGroup+resolution+codec's 63).
// They are deliberately unexported and not configurable — v1 has no
// per-installation tuning.
const (
	weightTitle         = 64
	weightYear          = 32
	weightSeasonEpisode = 16
	weightSource        = 8
	weightReleaseGroup  = 4
	weightResolution    = 2
	weightCodec         = 1

	// maxScore is the score assigned to a hash-matched Candidate: the sum
	// of every dimension's weight, guaranteeing it outranks any candidate
	// scored the ordinary way.
	maxScore = weightTitle + weightYear + weightSeasonEpisode + weightSource + weightReleaseGroup + weightResolution + weightCodec
)

// identityCutoff returns the minimum score a Candidate must reach to be
// eligible for selection: the sum of the identity attributes actually known
// for info. Cosmetic attributes (source, release group, resolution, codec)
// never gate acceptance — they only break ties among Candidates that
// already clear this cutoff.
//
// Year is always required for movies (see Parse), but for episodes it's
// only added to the cutoff when info.Year is actually known (non-zero) —
// mirroring Score's own year guard. Dropping it unconditionally for
// episodes would let a wrong-episode Candidate that happens to share a
// year cross a lowered cutoff on title+year alone; requiring it only when
// known preserves the lexicographic dominance (title > year >
// season/episode > cosmetics combined) that makes every identity attribute
// mandatory whenever it applies.
func identityCutoff(info Info) int {
	cutoff := weightTitle
	isEpisode := info.ContentType == media.Episode

	if isEpisode {
		cutoff += weightSeasonEpisode
		if info.Year != 0 {
			cutoff += weightYear
		}
	} else {
		cutoff += weightYear
	}

	return cutoff
}

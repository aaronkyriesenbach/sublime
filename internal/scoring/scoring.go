// Package scoring picks the single best domain.Candidate for a video, or
// correctly picks none, per CONTEXT.md's Candidate definition: a hash match
// short-circuits scoring entirely, and absent one, Candidates are scored
// against the video's own filename-derived metadata using fixed v1 weights,
// then filtered by a cutoff built from identity attributes alone. See
// CONTEXT.md at the repo root for domain vocabulary.
package scoring

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
// eligible for selection: the sum of the identity attributes for the given
// content type. Cosmetic attributes (source, release group, resolution,
// codec) never gate acceptance — they only break ties among Candidates that
// already clear this cutoff.
func identityCutoff(isEpisode bool) int {
	cutoff := weightTitle + weightYear
	if isEpisode {
		cutoff += weightSeasonEpisode
	}
	return cutoff
}

package scoring

import (
	"strings"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// Score computes candidate's score against info and whether it clears the
// eligibility cutoff (see identityCutoff). A HashMatch candidate short-
// circuits every other signal: it always scores maxScore and is always
// eligible, per CONTEXT.md's Candidate definition.
func Score(info Info, candidate domain.Candidate) (score int, eligible bool) {
	if candidate.HashMatch {
		return maxScore, true
	}

	isEpisode := info.ContentType == media.Episode

	if info.Title != "" && strings.EqualFold(info.Title, candidate.Title) {
		score += weightTitle
	}
	if info.Year != 0 && info.Year == candidate.Year {
		score += weightYear
	}
	if isEpisode && info.Season == candidate.Season && info.Episode == candidate.Episode {
		score += weightSeasonEpisode
	}

	score += cosmeticScore(weightSource, info.Source, candidate.Source)
	score += cosmeticScore(weightReleaseGroup, info.ReleaseGroup, candidate.ReleaseGroup)
	score += cosmeticScore(weightResolution, info.Resolution, candidate.Resolution)
	score += cosmeticScore(weightCodec, info.Codec, candidate.Codec)

	return score, score >= identityCutoff(isEpisode)
}

// cosmeticScore returns weight when both sides are non-empty and match
// case-insensitively, and 0 otherwise — an unrecognized or empty cosmetic
// attribute on either side scores 0 for that dimension without affecting
// eligibility, since cosmetic attributes never gate the cutoff.
func cosmeticScore(weight int, videoValue, candidateValue string) int {
	if videoValue == "" || candidateValue == "" {
		return 0
	}
	if strings.EqualFold(videoValue, candidateValue) {
		return weight
	}
	return 0
}

// Select returns the highest-scoring eligible Candidate for info among
// candidates, or ok=false if none clears the eligibility cutoff. Ties are
// broken in favor of the first candidate reaching the winning score, in
// candidates' original order.
func Select(info Info, candidates []domain.Candidate) (best domain.Candidate, ok bool) {
	bestScore := -1
	for _, candidate := range candidates {
		score, eligible := Score(info, candidate)
		if !eligible {
			continue
		}
		if score > bestScore {
			best, bestScore, ok = candidate, score, true
		}
	}
	return best, ok
}

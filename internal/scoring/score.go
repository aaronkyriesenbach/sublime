package scoring

import (
	"regexp"
	"strings"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// titleFoldPattern: \p{L}/\p{N} are Unicode letter/digit classes, so
// accented letters survive while punctuation, symbols, and whitespace
// (including a stray non-breaking space) don't.
var titleFoldPattern = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// foldTitle builds a title comparison key tolerant of cosmetic formatting
// differences only. It deliberately doesn't fold accents to a base letter,
// drop a leading "The"/"A"/"An", equate "&" with "and", or equate a numeral
// with its spelled-out word -- those are vocabulary-level differences with
// their own false-positive risk, left as genuine mismatches on purpose (see
// docs/adr/0009). Case is folded separately, by strings.EqualFold at this
// function's one call site.
func foldTitle(title string) string {
	return strings.TrimSpace(titleFoldPattern.ReplaceAllString(title, " "))
}

// Score computes candidate's score against info and whether it clears the
// eligibility cutoff (see identityCutoff). A HashMatch candidate short-
// circuits every other signal: it always scores maxScore and is always
// eligible, per CONTEXT.md's Candidate definition.
func Score(info Info, candidate domain.Candidate) (score int, eligible bool) {
	if candidate.HashMatch {
		return maxScore, true
	}

	isEpisode := info.ContentType == media.Episode

	if info.Title != "" && strings.EqualFold(foldTitle(info.Title), foldTitle(candidate.Title)) {
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

	return score, score >= identityCutoff(info)
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
	best, _, _, ok = bestOf(info, candidates, true)
	return best, ok
}

// Best returns the highest-raw-scoring Candidate among candidates
// regardless of eligibility, along with its score and the cutoff it needed
// to clear (see identityCutoff). ok is false only when candidates is empty.
// Callers use this for diagnostics when Select finds nothing eligible — it
// surfaces exactly what was decoded and how close it came, instead of just
// "no candidate".
func Best(info Info, candidates []domain.Candidate) (best domain.Candidate, score, cutoff int, ok bool) {
	return bestOf(info, candidates, false)
}

// bestOf is the shared scoring loop behind Select and Best. When
// eligibleOnly is true, ineligible candidates are skipped entirely
// (Select's contract); when false, every candidate is considered by raw
// score alone (Best's contract).
func bestOf(info Info, candidates []domain.Candidate, eligibleOnly bool) (best domain.Candidate, bestScore, cutoff int, ok bool) {
	cutoff = identityCutoff(info)
	bestScore = -1
	for _, candidate := range candidates {
		score, eligible := Score(info, candidate)
		if eligibleOnly && !eligible {
			continue
		}
		if score > bestScore {
			best, bestScore, ok = candidate, score, true
		}
	}
	return best, bestScore, cutoff, ok
}

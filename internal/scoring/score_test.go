package scoring_test

import (
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/scoring"
)

func TestScore_HashMatchShortCircuitsOtherSignals(t *testing.T) {
	info := scoring.Info{
		ContentType: media.Movie,
		Title:       "Arrival",
		Year:        2016,
		Source:      "BluRay",
	}
	// Every non-hash signal is wrong, yet HashMatch alone must still make
	// this Candidate eligible with the maximum score.
	candidate := domain.Candidate{
		Title:     "Completely Different Title",
		Year:      1999,
		Source:    "WEBRip",
		HashMatch: true,
	}

	score, eligible := scoring.Score(info, candidate)

	if !eligible {
		t.Fatalf("Score(...) eligible = false, want true for a HashMatch candidate")
	}
	if score <= 0 {
		t.Errorf("Score(...) score = %d, want a positive dominant score", score)
	}
}

func TestScore_CutoffRejectsWrongYear(t *testing.T) {
	info := scoring.Info{
		ContentType: media.Movie,
		Title:       "Arrival",
		Year:        2016,
		Source:      "BluRay",
		Resolution:  "1080p",
		Codec:       "x264",
	}
	// Title and every cosmetic attribute match, but the year doesn't — an
	// identity mismatch must reject the candidate regardless of how many
	// cosmetic attributes agree.
	candidate := domain.Candidate{
		Title:      "Arrival",
		Year:       1999,
		Source:     "BluRay",
		Resolution: "1080p",
		Codec:      "x264",
	}

	_, eligible := scoring.Score(info, candidate)

	if eligible {
		t.Errorf("Score(...) eligible = true, want false: cosmetic matches must not compensate for a missing identity attribute")
	}
}

func TestScore_UnrecognizedCosmeticAttributeScoresZeroButStaysEligible(t *testing.T) {
	info := scoring.Info{
		ContentType: media.Movie,
		Title:       "Arrival",
		Year:        2016,
		// Source left empty: the video's filename had no recognized source
		// token.
	}
	candidate := domain.Candidate{
		Title:  "Arrival",
		Year:   2016,
		Source: "BluRay",
	}

	score, eligible := scoring.Score(info, candidate)

	if !eligible {
		t.Errorf("Score(...) eligible = false, want true: a cosmetic-only gap must not reject an otherwise identity-matching candidate")
	}
	if want := 64 + 32; score != want {
		t.Errorf("Score(...) score = %d, want %d (title+year only, source contributes 0)", score, want)
	}
}

func TestSelect_TieBreaksOnCosmeticAttributes(t *testing.T) {
	info := scoring.Info{
		ContentType: media.Movie,
		Title:       "Arrival",
		Year:        2016,
		Source:      "BluRay",
		Resolution:  "1080p",
	}
	plain := domain.Candidate{ID: "plain", Title: "Arrival", Year: 2016}
	cosmeticMatch := domain.Candidate{ID: "cosmetic-match", Title: "Arrival", Year: 2016, Source: "BluRay", Resolution: "1080p"}

	best, ok := scoring.Select(info, []domain.Candidate{plain, cosmeticMatch})

	if !ok {
		t.Fatalf("Select(...) ok = false, want true")
	}
	if best.ID != cosmeticMatch.ID {
		t.Errorf("Select(...) = %q, want %q (more cosmetic attributes matched)", best.ID, cosmeticMatch.ID)
	}
}

func TestSelect_NoneEligibleReturnsNotOk(t *testing.T) {
	info := scoring.Info{ContentType: media.Movie, Title: "Arrival", Year: 2016}
	wrongYear := domain.Candidate{ID: "wrong-year", Title: "Arrival", Year: 1999}

	_, ok := scoring.Select(info, []domain.Candidate{wrongYear})

	if ok {
		t.Errorf("Select(...) ok = true, want false: no candidate clears the cutoff")
	}
}

func TestSelect_HashMatchWinsOverHigherCosmeticScore(t *testing.T) {
	info := scoring.Info{
		ContentType: media.Movie,
		Title:       "Arrival",
		Year:        2016,
		Source:      "BluRay",
		Resolution:  "1080p",
		Codec:       "x264",
	}
	// bestCosmetic matches every dimension ordinarily and would win a plain
	// score comparison, but hashMatch must still be selected since a hash
	// match discards every other signal.
	bestCosmetic := domain.Candidate{ID: "best-cosmetic", Title: "Arrival", Year: 2016, Source: "BluRay", Resolution: "1080p", Codec: "x264"}
	hashMatch := domain.Candidate{ID: "hash-match", Title: "Wrong Title", Year: 1900, HashMatch: true}

	best, ok := scoring.Select(info, []domain.Candidate{bestCosmetic, hashMatch})

	if !ok {
		t.Fatalf("Select(...) ok = false, want true")
	}
	if best.ID != hashMatch.ID {
		t.Errorf("Select(...) = %q, want %q (hash match dominates)", best.ID, hashMatch.ID)
	}
}

func TestScore_EpisodeCutoffRejectsWrongEpisode(t *testing.T) {
	info := scoring.Info{
		ContentType: media.Episode,
		Title:       "Show Name",
		Year:        2020,
		Season:      1,
		Episode:     2,
	}
	// Title and year match, but the episode number doesn't — season/episode
	// is an identity attribute for episodes, so this must still be rejected.
	candidate := domain.Candidate{Title: "Show Name", Year: 2020, Season: 1, Episode: 5}

	_, eligible := scoring.Score(info, candidate)

	if eligible {
		t.Errorf("Score(...) eligible = true, want false: wrong episode number must reject the candidate")
	}
}

func TestScore_EpisodeMatchingSeasonEpisodeContributesWeight(t *testing.T) {
	info := scoring.Info{ContentType: media.Episode, Title: "Show Name", Year: 2020, Season: 1, Episode: 2}
	candidate := domain.Candidate{Title: "Show Name", Year: 2020, Season: 1, Episode: 2}

	score, eligible := scoring.Score(info, candidate)

	if !eligible {
		t.Fatalf("Score(...) eligible = false, want true")
	}
	if want := 64 + 32 + 16; score != want {
		t.Errorf("Score(...) score = %d, want %d (title+year+season/episode)", score, want)
	}
}

func TestScore_CosmeticMismatchBothPresentScoresZero(t *testing.T) {
	info := scoring.Info{ContentType: media.Movie, Title: "Arrival", Year: 2016, Source: "BluRay"}
	candidate := domain.Candidate{Title: "Arrival", Year: 2016, Source: "WEBRip"}

	score, eligible := scoring.Score(info, candidate)

	if !eligible {
		t.Errorf("Score(...) eligible = false, want true")
	}
	if want := 64 + 32; score != want {
		t.Errorf("Score(...) score = %d, want %d (mismatched source contributes 0)", score, want)
	}
}

func TestScore_EpisodeWithUnknownYearEligibleOnTitleAndSeasonEpisode(t *testing.T) {
	info := scoring.Info{ContentType: media.Episode, Title: "Community", Season: 2, Episode: 1}
	candidate := domain.Candidate{Title: "Community", Year: 2010, Season: 2, Episode: 1}

	score, eligible := scoring.Score(info, candidate)

	if !eligible {
		t.Fatalf("Score(...) eligible = false, want true: title+season/episode is the full identity when the video's year is unknown")
	}
	if want := 64 + 16; score != want {
		t.Errorf("Score(...) score = %d, want %d (title+season/episode; the candidate's year must not be scored against an unknown video year)", score, want)
	}
}

// TestScore_EpisodeWithUnknownYearStillRejectsWrongEpisode is the
// regression guard for the fix: dropping year from an episode's cutoff
// when the video's year is unknown must not let a Candidate's own year
// compensate for a season/episode mismatch — season/episode must remain a
// hard identity gate.
func TestScore_EpisodeWithUnknownYearStillRejectsWrongEpisode(t *testing.T) {
	info := scoring.Info{ContentType: media.Episode, Title: "Community", Season: 2, Episode: 1}
	candidate := domain.Candidate{Title: "Community", Year: 2010, Season: 2, Episode: 5}

	_, eligible := scoring.Score(info, candidate)

	if eligible {
		t.Errorf("Score(...) eligible = true, want false: wrong episode number must reject the candidate even when the video's year is unknown")
	}
}

func TestBest_ReturnsTopScoringCandidateEvenWhenIneligible(t *testing.T) {
	info := scoring.Info{ContentType: media.Movie, Title: "Blue Mountain State: The Rise of Thadland", Year: 2016}
	// Punctuation dropped from the filename-derived title never matches
	// info.Title's exact EqualFold check, so this candidate is ineligible --
	// but it's still the only, and therefore top-scoring, candidate.
	candidate := domain.Candidate{ID: "only-candidate", Title: "Blue Mountain State the Rise of Thadland", Year: 2016}

	best, score, cutoff, ok := scoring.Best(info, []domain.Candidate{candidate})

	if !ok {
		t.Fatalf("Best(...) ok = false, want true")
	}
	if best.ID != candidate.ID {
		t.Errorf("Best(...) = %q, want %q", best.ID, candidate.ID)
	}
	if want := 32; score != want { // year matches (32), title doesn't (0)
		t.Errorf("Best(...) score = %d, want %d", score, want)
	}
	if want := 64 + 32; cutoff != want { // movie cutoff: title + year
		t.Errorf("Best(...) cutoff = %d, want %d", cutoff, want)
	}
	if score >= cutoff {
		t.Errorf("score %d >= cutoff %d, want the candidate to actually miss", score, cutoff)
	}
}

func TestBest_PicksHighestRawScoreAmongMultipleIneligibleCandidates(t *testing.T) {
	info := scoring.Info{ContentType: media.Movie, Title: "Arrival", Year: 2016}
	worse := domain.Candidate{ID: "worse", Title: "Wrong Title", Year: 1999}
	better := domain.Candidate{ID: "better", Title: "Wrong Title", Year: 2016}

	best, score, _, ok := scoring.Best(info, []domain.Candidate{worse, better})

	if !ok {
		t.Fatalf("Best(...) ok = false, want true")
	}
	if best.ID != better.ID {
		t.Errorf("Best(...) = %q, want %q (matches year, the higher-scoring signal)", best.ID, better.ID)
	}
	if want := 32; score != want {
		t.Errorf("Best(...) score = %d, want %d", score, want)
	}
}

func TestBest_EmptyCandidatesReturnsNotOk(t *testing.T) {
	info := scoring.Info{ContentType: media.Movie, Title: "Arrival", Year: 2016}

	_, _, _, ok := scoring.Best(info, nil)

	if ok {
		t.Errorf("Best(...) ok = true, want false: no candidates to report")
	}
}

func TestBest_EligibleCandidateStillReportedWithOkTrue(t *testing.T) {
	info := scoring.Info{ContentType: media.Movie, Title: "Arrival", Year: 2016}
	match := domain.Candidate{ID: "match", Title: "Arrival", Year: 2016}

	best, score, cutoff, ok := scoring.Best(info, []domain.Candidate{match})

	if !ok {
		t.Fatalf("Best(...) ok = false, want true")
	}
	if best.ID != match.ID {
		t.Errorf("Best(...) = %q, want %q", best.ID, match.ID)
	}
	if score < cutoff {
		t.Errorf("score %d < cutoff %d, want an eligible candidate to clear it", score, cutoff)
	}
}

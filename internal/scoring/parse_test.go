package scoring_test

import (
	"errors"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/scoring"
)

func TestParse_MovieSceneRelease(t *testing.T) {
	info, err := scoring.Parse("Arrival.2016.1080p.BluRay.x264-GROUP.mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	want := scoring.Info{
		ContentType:  media.Movie,
		Title:        "Arrival",
		Year:         2016,
		Source:       "BluRay",
		ReleaseGroup: "GROUP",
		Resolution:   "1080p",
		Codec:        "x264",
	}
	if info != want {
		t.Errorf("Parse(...) = %+v, want %+v", info, want)
	}
}

// TestParse_TitleContainingAYearLikeNumber guards against the classic
// ambiguous case: a title ("Blade Runner 2049") that itself contains a
// 4-digit number shaped just like a release year. The actual release year
// (2017) is the last year-shaped token before the first cosmetic tag, so
// the earlier one must fold back into the title instead.
func TestParse_TitleContainingAYearLikeNumber(t *testing.T) {
	info, err := scoring.Parse("Blade.Runner.2049.2017.2160p.WEB-DL.mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	want := scoring.Info{
		ContentType: media.Movie,
		Title:       "Blade Runner 2049",
		Year:        2017,
		Source:      "WEB-DL",
		Resolution:  "2160p",
	}
	if info != want {
		t.Errorf("Parse(...) = %+v, want %+v", info, want)
	}
}

func TestParse_MovieWithParenthesizedYearAndNoCosmeticTags(t *testing.T) {
	info, err := scoring.Parse("The Matrix (1999).mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	want := scoring.Info{ContentType: media.Movie, Title: "The Matrix", Year: 1999}
	if info != want {
		t.Errorf("Parse(...) = %+v, want %+v", info, want)
	}
}

func TestParse_EpisodeSceneRelease(t *testing.T) {
	info, err := scoring.Parse("Show.Name.2020.S01E02.1080p.WEB-DL.mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	want := scoring.Info{
		ContentType: media.Episode,
		Title:       "Show Name",
		Year:        2020,
		Season:      1,
		Episode:     2,
		Source:      "WEB-DL",
		Resolution:  "1080p",
	}
	if info != want {
		t.Errorf("Parse(...) = %+v, want %+v", info, want)
	}
}

// TestParse_EpisodeWithoutYearIsNotAnIdentityFailure guards the fix for TV
// filenames — unlike movies, a missing year is not fatal for an episode:
// Season/Episode already disambiguates it within its show, and this naming
// style (no year at all) is extremely common for TV rips.
func TestParse_EpisodeWithoutYearIsNotAnIdentityFailure(t *testing.T) {
	info, err := scoring.Parse("Community - S02E01 - Anthropology 101.mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	want := scoring.Info{
		ContentType: media.Episode,
		Title:       "Community",
		Season:      2,
		Episode:     1,
	}
	if info != want {
		t.Errorf("Parse(...) = %+v, want %+v", info, want)
	}
}

func TestParse_MissingYearIsIdentityFailure(t *testing.T) {
	_, err := scoring.Parse("Some.Movie.Title.mkv")

	if !errors.Is(err, scoring.ErrIdentityAttribute) {
		t.Errorf("Parse(...) error = %v, want wrapping ErrIdentityAttribute", err)
	}
}

func TestParse_MissingTitleIsIdentityFailure(t *testing.T) {
	_, err := scoring.Parse("2016.1080p.BluRay.x264-GROUP.mkv")

	if !errors.Is(err, scoring.ErrIdentityAttribute) {
		t.Errorf("Parse(...) error = %v, want wrapping ErrIdentityAttribute", err)
	}
}

func TestParse_UnrecognizedCosmeticAttributesAreLeftEmpty(t *testing.T) {
	info, err := scoring.Parse("Arrival.2016.mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	want := scoring.Info{ContentType: media.Movie, Title: "Arrival", Year: 2016}
	if info != want {
		t.Errorf("Parse(...) = %+v, want %+v (unrecognized cosmetic attributes left empty)", info, want)
	}
}

func TestParse_SpecialEpisodeWithZeroSeasonAndEpisodeIsIdentityFailure(t *testing.T) {
	_, err := scoring.Parse("Show.Name.2020.S00E00.1080p.WEB-DL.mkv")

	if !errors.Is(err, scoring.ErrIdentityAttribute) {
		t.Errorf("Parse(...) error = %v, want wrapping ErrIdentityAttribute", err)
	}
}

// TestParse_HyphenSuffixThatIsItselfATagYieldsNoReleaseGroup covers the
// literal "skipped if it's itself a recognized tag" rule: the token after
// the last hyphen ("720p") is a recognized resolution tag on its own, so it
// must not be mistaken for a release group.
func TestParse_HyphenSuffixThatIsItselfATagYieldsNoReleaseGroup(t *testing.T) {
	info, err := scoring.Parse("Movie.2020.BluRay-720p.mkv")
	if err != nil {
		t.Fatalf("Parse(...) error = %v, want nil", err)
	}

	if info.ReleaseGroup != "" {
		t.Errorf("Parse(...).ReleaseGroup = %q, want empty (suffix is itself a recognized tag)", info.ReleaseGroup)
	}
}

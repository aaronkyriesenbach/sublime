package media_test

import (
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/media"
)

func TestClassify_MovieDefault(t *testing.T) {
	cases := []string{
		"Arrival.2016.1080p.BluRay.x264-GROUP.mkv",
		"The Matrix (1999).mkv",
		"Blade Runner 2049.2017.2160p.WEB-DL.mkv",
		"Some.Movie.Title.mp4",
	}

	for _, name := range cases {
		got := media.Classify(name)
		if got.Type != media.Movie {
			t.Errorf("Classify(%q).Type = %v, want Movie", name, got.Type)
		}
	}
}

func TestClassify_SxxEyyStyle(t *testing.T) {
	cases := []struct {
		name    string
		season  int
		episode int
	}{
		{"Show.Name.S01E02.1080p.WEB-DL.mkv", 1, 2},
		{"Show.Name.S1E2.mkv", 1, 2},
		{"show.name.s10e23.mkv", 10, 23},
		{"Show Name - S02E15 - Episode Title.mkv", 2, 15},
		{"Anime.Show.S01E999.mkv", 1, 999},
	}

	for _, tc := range cases {
		got := media.Classify(tc.name)
		if got.Type != media.Episode {
			t.Fatalf("Classify(%q).Type = %v, want Episode", tc.name, got.Type)
		}
		if got.Season != tc.season || got.Episode != tc.episode {
			t.Errorf("Classify(%q) = season %d episode %d, want season %d episode %d",
				tc.name, got.Season, got.Episode, tc.season, tc.episode)
		}
	}
}

func TestClassify_NxYYStyle(t *testing.T) {
	cases := []struct {
		name    string
		season  int
		episode int
	}{
		{"Show.Name.1x02.mkv", 1, 2},
		{"Show Name - 1x02 - Episode Title.mkv", 1, 2},
		{"show.name.12x03.mkv", 12, 3},
	}

	for _, tc := range cases {
		got := media.Classify(tc.name)
		if got.Type != media.Episode {
			t.Fatalf("Classify(%q).Type = %v, want Episode", tc.name, got.Type)
		}
		if got.Season != tc.season || got.Episode != tc.episode {
			t.Errorf("Classify(%q) = season %d episode %d, want season %d episode %d",
				tc.name, got.Season, got.Episode, tc.season, tc.episode)
		}
	}
}

// TestClassify_ResolutionNotMistakenForEpisode guards against the 1x02-style
// pattern false-matching resolution tokens like "1920x1080", which share the
// same "digits x digits" shape.
func TestClassify_ResolutionNotMistakenForEpisode(t *testing.T) {
	cases := []string{
		"Movie.Name.1920x1080p.mkv",
		"Movie.Name.720x480.mkv",
	}

	for _, name := range cases {
		got := media.Classify(name)
		if got.Type != media.Movie {
			t.Errorf("Classify(%q).Type = %v, want Movie (resolution token misread as episode)", name, got.Type)
		}
	}
}

// TestClassify_IgnoresDirectoryStructure confirms the classifier looks only
// at the filename, never at parent directory names, even when a full path
// is passed in.
func TestClassify_IgnoresDirectoryStructure(t *testing.T) {
	got := media.Classify("/media/tv/Show Name/Season 01/Show.Name.Episode.Title.mkv")
	if got.Type != media.Movie {
		t.Errorf("Classify with TV-shaped directory but no episode token in filename = %v, want Movie", got.Type)
	}
}

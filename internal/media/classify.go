package media

import (
	"path/filepath"
	"regexp"
	"strconv"
)

// ContentType classifies a video file as a movie or a TV episode.
type ContentType int

const (
	// Movie is the default classification: no episode token was recognized
	// in the filename. There is no config hint or directory-structure
	// fallback — an unrecognized filename is always a movie.
	Movie ContentType = iota
	Episode
)

func (t ContentType) String() string {
	switch t {
	case Episode:
		return "Episode"
	default:
		return "Movie"
	}
}

// Classification is the result of classifying a video file's filename.
// Season and Episode are only meaningful when Type is Episode.
type Classification struct {
	Type    ContentType
	Season  int
	Episode int
}

var (
	// sxxeyyPattern matches "S01E02"-style episode tokens, case-insensitive.
	sxxeyyPattern = regexp.MustCompile(`(?i)\bs(\d{1,2})e(\d{1,3})\b`)

	// nxyyPattern matches "1x02"-style episode tokens. The season group is
	// capped at 2 digits so a resolution token like "1920x1080" (whose 'x'
	// only follows 4 digits) can never satisfy it.
	nxyyPattern = regexp.MustCompile(`\b(\d{1,2})x(\d{1,3})\b`)
)

// Classify classifies a video by its filename alone. Any leading directory
// components in name are ignored, and an unrecognized filename always
// classifies as Movie.
func Classify(name string) Classification {
	base := filepath.Base(name)

	if m := sxxeyyPattern.FindStringSubmatch(base); m != nil {
		return Classification{
			Type:    Episode,
			Season:  atoi(m[1]),
			Episode: atoi(m[2]),
		}
	}

	if m := nxyyPattern.FindStringSubmatch(base); m != nil {
		return Classification{
			Type:    Episode,
			Season:  atoi(m[1]),
			Episode: atoi(m[2]),
		}
	}

	return Classification{Type: Movie}
}

// atoi parses a regexp-captured run of digits, which is always valid input.
func atoi(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}

package scoring

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// ErrIdentityAttribute is wrapped by the error Parse returns when a
// filename doesn't yield one of the required identity attributes (title,
// year, or for episodes, season/episode). Per CONTEXT.md, an unextractable
// identity attribute is a terminal failure recorded before scoring starts —
// callers should treat it as unscoreable, not attempt to score it anyway.
var ErrIdentityAttribute = errors.New("scoring: filename is missing a required identity attribute")

var (
	// yearToken matches a plausible release-year token: 1900-2099.
	yearToken = regexp.MustCompile(`^(19|20)\d{2}$`)

	// episodeToken matches the same two season/episode shapes
	// media.Classify recognizes (S01E02 and 1x02 styles), used here only to
	// find where a filename's metadata run begins — media.Classify remains
	// the authority for the actual Season/Episode values.
	episodeToken = regexp.MustCompile(`(?i)^(s\d{1,2}e\d{1,3}|\d{1,2}x\d{1,3})$`)

	// sourceToken, resolutionToken, and codecToken enumerate the scene-
	// release naming conventions this parser recognizes for each cosmetic
	// dimension.
	sourceToken     = regexp.MustCompile(`(?i)^(bluray|web-dl|webrip|hdtv|dvdrip)$`)
	resolutionToken = regexp.MustCompile(`(?i)^(2160p|1080p|720p|480p)$`)
	codecToken      = regexp.MustCompile(`(?i)^(x264|x265|hevc|av1)$`)
)

// Info is the filename-derived metadata for a video Sublime is finding
// subtitles for: identity attributes (title, year, and for episodes,
// season/episode) plus cosmetic attributes (source, release group,
// resolution, codec). It mirrors domain.Candidate's own fields so a video
// can be scored against its Candidates attribute-by-attribute. Produced by
// Parse.
type Info struct {
	ContentType media.ContentType

	// Title, Year, Season, and Episode are this video's identity, as
	// extracted from its filename. Season and Episode are zero and
	// unused when ContentType is media.Movie.
	Title   string
	Year    int
	Season  int
	Episode int

	// Source, ReleaseGroup, Resolution, and Codec are this video's
	// cosmetic attributes, as extracted from its filename. Any of these
	// may be empty when the filename doesn't use a recognized token for
	// that dimension.
	Source       string
	ReleaseGroup string
	Resolution   string
	Codec        string
}

// Parse extracts a video's identity and cosmetic metadata from its
// filename, reusing media.Classify for content-type/season/episode
// detection and a bespoke token parser for everything else. It returns an
// error wrapping ErrIdentityAttribute if title or (for episodes)
// season/episode can't be extracted. A year is also required for movies,
// but not for episodes, where Season/Episode already disambiguates the
// file and a filename commonly omits it; cosmetic attributes never cause
// an error — an unrecognized one is simply left empty.
func Parse(filename string) (Info, error) {
	base := filepath.Base(filename)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	classification := media.Classify(base)
	isEpisode := classification.Type == media.Episode
	if isEpisode && classification.Season == 0 && classification.Episode == 0 {
		return Info{}, fmt.Errorf("%w: season/episode not found in %q", ErrIdentityAttribute, filename)
	}

	group, remaining := extractReleaseGroup(stem)

	tokens := tokenize(remaining)

	metaStart := len(tokens)
	for i, tok := range tokens {
		if isRecognizedTag(tok) || episodeToken.MatchString(tok) {
			metaStart = i
			break
		}
	}

	yearIdx := -1
	year := 0
	for i := 0; i < metaStart; i++ {
		if yearToken.MatchString(tokens[i]) {
			yearIdx = i
			year, _ = strconv.Atoi(tokens[i])
		}
	}

	// A year is a required identity attribute for movies (see CONTEXT.md's
	// Candidate entry) — titles alone don't disambiguate remakes/re-releases
	// that share a title across different years. Episodes are different:
	// Season/Episode already disambiguates the file within its show, and TV
	// release filenames very commonly omit a year altogether, so its absence
	// there falls back to the metadata run's start (the episode token, or a
	// cosmetic tag) as the title boundary instead of erroring.
	titleEnd := yearIdx
	if titleEnd == -1 {
		if !isEpisode {
			return Info{}, fmt.Errorf("%w: year not found in %q", ErrIdentityAttribute, filename)
		}
		titleEnd = metaStart
	}

	title := strings.Join(tokens[:titleEnd], " ")
	if title == "" {
		return Info{}, fmt.Errorf("%w: title not found in %q", ErrIdentityAttribute, filename)
	}

	info := Info{
		ContentType:  classification.Type,
		Title:        title,
		Year:         year,
		ReleaseGroup: group,
	}
	if isEpisode {
		info.Season = classification.Season
		info.Episode = classification.Episode
	}
	for _, tok := range tokens {
		switch {
		case info.Source == "" && sourceToken.MatchString(tok):
			info.Source = tok
		case info.Resolution == "" && resolutionToken.MatchString(tok):
			info.Resolution = tok
		case info.Codec == "" && codecToken.MatchString(tok):
			info.Codec = tok
		}
	}

	return info, nil
}

// isRecognizedTag reports whether token is, as a whole, one of the
// recognized source, resolution, or codec tokens.
func isRecognizedTag(token string) bool {
	return sourceToken.MatchString(token) || resolutionToken.MatchString(token) || codecToken.MatchString(token)
}

// extractReleaseGroup splits the last hyphen-delimited token off stem's
// final dot-segment as the release group, per scene-release convention
// (e.g. "x264-GROUP"). It returns ("", stem) unchanged when: the final
// segment has no hyphen; it contains a space (a natural-language separator
// like " - ", not a tight scene-style hyphen); the segment as a whole is
// itself a recognized tag (e.g. "WEB-DL", the one recognized source tag
// with an internal hyphen); or the candidate token after the last hyphen is
// itself a recognized tag.
func extractReleaseGroup(stem string) (group string, remaining string) {
	prefix := ""
	lastSeg := stem
	if i := strings.LastIndex(stem, "."); i != -1 {
		prefix = stem[:i+1]
		lastSeg = stem[i+1:]
	}

	if !strings.Contains(lastSeg, "-") || strings.Contains(lastSeg, " ") || isRecognizedTag(lastSeg) {
		return "", stem
	}

	i := strings.LastIndex(lastSeg, "-")
	segPrefix, segSuffix := lastSeg[:i], lastSeg[i+1:]
	if isRecognizedTag(segSuffix) {
		return "", stem
	}

	return segSuffix, prefix + segPrefix
}

// tokenize splits s into cleaned, non-empty tokens: "." and "_" are treated
// as word separators alongside whitespace, and each token has surrounding
// punctuation (parentheses, brackets, stray hyphens) trimmed.
func tokenize(s string) []string {
	replacer := strings.NewReplacer(".", " ", "_", " ")
	fields := strings.Fields(replacer.Replace(s))

	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if cleaned := strings.Trim(f, "()[]{}-"); cleaned != "" {
			tokens = append(tokens, cleaned)
		}
	}
	return tokens
}

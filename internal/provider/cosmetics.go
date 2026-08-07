package provider

import (
	"regexp"
	"strings"
)

// cosmeticSourceToken, cosmeticResolutionToken, and cosmeticCodecToken
// match scene-release tokens for video source, resolution, and codec.
// Mirrors the filename attribute extraction decision (#29): the same
// subliminal/Bazarr scene-release prior art, applied to OpenSubtitles'
// release strings instead of Sublime's video filenames.
var (
	cosmeticSourceToken     = regexp.MustCompile(`(?i)^(bluray|web-dl|webrip|hdtv|dvdrip)$`)
	cosmeticResolutionToken = regexp.MustCompile(`(?i)^(2160p|1080p|720p|480p)$`)
	cosmeticCodecToken      = regexp.MustCompile(`(?i)^(x264|x265|hevc|av1)$`)
)

// ParseCosmetics extracts cosmetic attributes from a scene-release-style
// release string (e.g. "Arrival.2016.1080p.BluRay.x264-GROUP"). Any
// dimension without a recognized token is left empty, matching Sublime's
// "missing cosmetic attribute scores 0" convention (internal/scoring)
// rather than erroring. Usable by any Provider package, not just
// OpenSubtitles.
func ParseCosmetics(release string) (source, releaseGroup, resolution, codec string) {
	releaseGroup, remaining := extractCosmeticReleaseGroup(release)

	replacer := strings.NewReplacer(".", " ", "_", " ")
	for _, tok := range strings.Fields(replacer.Replace(remaining)) {
		tok = strings.Trim(tok, "()[]{}")
		switch {
		case source == "" && cosmeticSourceToken.MatchString(tok):
			source = tok
		case resolution == "" && cosmeticResolutionToken.MatchString(tok):
			resolution = tok
		case codec == "" && cosmeticCodecToken.MatchString(tok):
			codec = tok
		}
	}
	return source, releaseGroup, resolution, codec
}

func isRecognizedCosmeticTag(token string) bool {
	return cosmeticSourceToken.MatchString(token) || cosmeticResolutionToken.MatchString(token) || cosmeticCodecToken.MatchString(token)
}

// extractCosmeticReleaseGroup splits the last hyphen-delimited token off
// release's final dot-segment as the release group, per scene-release
// convention (e.g. "x264-GROUP") — the same heuristic as scoring's filename
// parser (#29), applied to release strings (e.g. from OpenSubtitles) instead
// of a filename. Returns empty group and the original release if the suffix
// would be empty or ambiguous.
func extractCosmeticReleaseGroup(release string) (group string, remaining string) {
	prefix := ""
	lastSeg := release
	if i := strings.LastIndex(release, "."); i != -1 {
		prefix = release[:i+1]
		lastSeg = release[i+1:]
	}

	if !strings.Contains(lastSeg, "-") || strings.Contains(lastSeg, " ") || isRecognizedCosmeticTag(lastSeg) {
		return "", release
	}

	i := strings.LastIndex(lastSeg, "-")
	segPrefix, segSuffix := lastSeg[:i], lastSeg[i+1:]
	if isRecognizedCosmeticTag(segSuffix) {
		return "", release
	}

	return segSuffix, prefix + segPrefix
}

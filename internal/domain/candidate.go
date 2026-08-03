package domain

// Candidate is a subtitle result returned by a Provider search, not yet
// chosen. See CONTEXT.md for the canonical definition. A scoring/matching
// engine ranks Candidates against a video's own metadata; the Provider
// package (internal/provider) is what produces them.
//
// Season and Episode are zero for movies. TV episodes always set Episode;
// Season may still be zero for shows whose own numbering omits it (e.g.
// some anime).
type Candidate struct {
	// ID is an opaque, Provider-specific identifier for this Candidate. It
	// carries no meaning outside the Provider that returned it and plays no
	// part in scoring; callers pass the Candidate back to Provider.Download
	// unchanged, and a Provider may embed whatever lookup state it needs in
	// ID.
	ID string

	// Title, Year, Season, and Episode are this Candidate's own claimed
	// identity — scored against the video's identity to confirm the
	// Candidate is actually for the right title, year, and episode.
	Title   string
	Year    int
	Season  int
	Episode int

	// Source, ReleaseGroup, Resolution, and Codec are this Candidate's
	// cosmetic attributes — scored to prefer subtitles that match the
	// video's own release characteristics (e.g. a BluRay subtitle over a
	// WEBRip one for a BluRay video).
	Source       string
	ReleaseGroup string
	Resolution   string
	Codec        string

	// HashMatch reports whether the Provider's own internal hash lookup
	// (independent of Sublime's Content Hash — see CONTEXT.md) matched this
	// Candidate exactly against the queried video. A hash match is meant to
	// short-circuit scoring: all other signals are discarded in that case.
	HashMatch bool
}

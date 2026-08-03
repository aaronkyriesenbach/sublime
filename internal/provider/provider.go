// Package provider defines Sublime's Provider port — the pluggable
// interface for subtitle retrieval described in CONTEXT.md — and a Fake
// implementation used to drive the rest of the pipeline's tests without a
// network dependency. Real implementations (e.g. OpenSubtitles) satisfy
// this interface from their own packages.
package provider

import (
	"context"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// Query describes the video Sublime wants subtitles for: the identity
// attributes a Provider searches by, plus the target subtitle language.
// Season and Episode are zero for movies.
type Query struct {
	Title    string
	Year     int
	Season   int
	Episode  int
	Language language.Tag
}

// Provider is an external subtitle source Sublime can query for Candidates
// (see CONTEXT.md). Sublime supports one Provider in v1, but the interface
// is built to support several: each implementation owns its own rate
// limiting and auth, and derives whatever hash or lookup key it needs from
// the video independently of Sublime's Content Hash.
type Provider interface {
	// Search returns the Candidates found for query, in no particular
	// order. A nil or empty slice with a nil error means the Provider
	// understood the query but found nothing — that is not an error
	// condition.
	Search(ctx context.Context, query Query) ([]domain.Candidate, error)

	// Download retrieves the raw subtitle content for candidate, which must
	// be a Candidate previously returned by Search on this same Provider —
	// implementations may rely on state embedded in Candidate.ID.
	Download(ctx context.Context, candidate domain.Candidate) ([]byte, error)
}

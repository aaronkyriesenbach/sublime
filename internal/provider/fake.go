package provider

import (
	"context"
	"fmt"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// Fake is a Provider that returns pre-configured (canned) results instead of
// calling a real subtitle source, so the rest of the pipeline can be
// exercised in tests without a network dependency.
//
// Configure SearchFunc and/or DownloadFunc per test to canned closures. A
// nil SearchFunc yields a Search that finds nothing, matching a real
// Provider's "no candidates" outcome; a nil DownloadFunc yields a Download
// that errors, since downloading a Candidate no test configured is a test
// setup mistake rather than a legitimate empty result.
type Fake struct {
	SearchFunc   func(ctx context.Context, query Query) ([]domain.Candidate, error)
	DownloadFunc func(ctx context.Context, candidate domain.Candidate) ([]byte, error)
}

var _ Provider = (*Fake)(nil)

// Search delegates to SearchFunc, or reports no Candidates and no error if
// unset.
func (f *Fake) Search(ctx context.Context, query Query) ([]domain.Candidate, error) {
	if f.SearchFunc == nil {
		return nil, nil
	}
	return f.SearchFunc(ctx, query)
}

// Download delegates to DownloadFunc, or reports an error if unset.
func (f *Fake) Download(ctx context.Context, candidate domain.Candidate) ([]byte, error) {
	if f.DownloadFunc == nil {
		return nil, fmt.Errorf("provider: fake has no DownloadFunc configured for candidate %q", candidate.ID)
	}
	return f.DownloadFunc(ctx, candidate)
}

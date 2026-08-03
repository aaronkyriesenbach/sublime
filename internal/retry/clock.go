// Package retry provides a generic retry/backoff executor for recovering
// from transient failures in arbitrary operations without hammering
// whatever is on the other end (e.g. a Provider). See CONTEXT.md and
// decision #16 for the failure-handling model this package implements.
package retry

import (
	"context"
	"time"
)

// Clock abstracts time so retry/backoff delays can be awaited without
// depending on the real wall clock. Production code uses RealClock; tests
// use a fake that records requested delays instead of actually sleeping,
// so retry timing tests run instantly.
type Clock interface {
	// Sleep pauses for d, or returns ctx.Err() if ctx is done first.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is a Clock backed by the real wall clock.
type RealClock struct{}

// Sleep blocks for d, or returns early with ctx.Err() if ctx is cancelled
// first.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

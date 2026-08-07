package provider

import (
	"fmt"
	"time"
)

// QuotaExhaustedError signals that a Provider has hit an external quota
// limit (e.g. a daily download cap) and should not be called again until
// ResumeAt. It is Provider-agnostic — defined here rather than in any
// specific Provider's own package (e.g. opensubtitles) — so callers of the
// Provider interface can recognize the condition without depending on an
// implementation-specific error type, and so any current or future
// Provider implementation can report the same condition.
type QuotaExhaustedError struct {
	// ResumeAt is when the Provider expects its quota to reset and calls to
	// start succeeding again.
	ResumeAt time.Time

	// Cause is the underlying, Provider-specific error that revealed the
	// quota exhaustion, if any (e.g. the raw API error response). May be
	// nil.
	Cause error
}

func (e *QuotaExhaustedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("provider: quota exhausted until %s: %v", e.ResumeAt.Format(time.RFC3339), e.Cause)
	}
	return fmt.Sprintf("provider: quota exhausted until %s", e.ResumeAt.Format(time.RFC3339))
}

// Unwrap exposes Cause so errors.Is/errors.As can see through to the
// Provider-specific error that triggered the suspension.
func (e *QuotaExhaustedError) Unwrap() error {
	return e.Cause
}

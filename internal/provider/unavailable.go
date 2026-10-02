package provider

import (
	"fmt"
	"time"
)

// UnavailableError signals that a Provider cannot be reached at all (e.g.
// a speech-recognition sidecar that is down) and should not be called
// again until ResumeAt. Like QuotaExhaustedError it is Provider-agnostic:
// callers of the Provider interface treat it as a Suspension rather than a
// failure of the (file, language) pair that happened to hit it.
type UnavailableError struct {
	// ResumeAt is when the Provider expects to be reachable again, as a
	// conservative guess; the Suspension ends earlier only if a request
	// succeeds.
	ResumeAt time.Time

	// Cause is the underlying connection error that revealed the outage.
	// May be nil.
	Cause error
}

func (e *UnavailableError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("provider: unavailable until %s: %v", e.ResumeAt.Format(time.RFC3339), e.Cause)
	}
	return fmt.Sprintf("provider: unavailable until %s", e.ResumeAt.Format(time.RFC3339))
}

// Unwrap exposes Cause so errors.Is/errors.As can see through to the
// connection error that triggered the suspension.
func (e *UnavailableError) Unwrap() error {
	return e.Cause
}

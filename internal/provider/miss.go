package provider

import "fmt"

// MissError signals that a Provider found no usable subtitle for a pair
// after Search had offered a Candidate: Download produced nothing a pair
// could ship (e.g. a transcript that failed its sanity check). Callers treat
// it like a Search miss, not a failure of the pair: the Provider is recorded
// as tried and the Provider Chain advances.
type MissError struct {
	// Cause says why there is no usable subtitle, for the log.
	Cause error
}

func (e *MissError) Error() string {
	return fmt.Sprintf("provider: no usable subtitle: %v", e.Cause)
}

// Unwrap exposes Cause to errors.Is/errors.As.
func (e *MissError) Unwrap() error {
	return e.Cause
}

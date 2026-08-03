package retry

import (
	"context"
	"errors"
	"time"
)

// Policy holds the tunable retry/backoff parameters. Use DefaultPolicy for
// the project's agreed defaults (decision #16): 3 retries, exponential
// backoff 1s->2s->4s capped at 10s with full jitter, and a Provider delay
// hint cap of 60s.
type Policy struct {
	MaxRetries       int
	BaseDelay        time.Duration
	MaxDelay         time.Duration
	MaxProviderDelay time.Duration
	Jitter           func(max time.Duration) time.Duration
}

// DefaultPolicy returns the project's agreed retry/backoff parameters.
func DefaultPolicy() Policy {
	return Policy{
		MaxRetries:       3,
		BaseDelay:        time.Second,
		MaxDelay:         10 * time.Second,
		MaxProviderDelay: 60 * time.Second,
		Jitter:           FullJitter,
	}
}

// TransientError marks an error returned by an Operation as transient -
// worth retrying (timeouts, 5xx responses, rate-limit responses, and the
// like). Any error not wrapped as a TransientError is treated as terminal:
// Do records it immediately and never retries automatically.
type TransientError struct {
	err        error
	retryAfter time.Duration
}

// Transient wraps err to mark it as transient.
func Transient(err error) error {
	return &TransientError{err: err}
}

// TransientAfter wraps err to mark it as transient, carrying a
// Provider-supplied delay hint (e.g. Retry-After) that overrides the
// computed backoff delay when the executor next waits before retrying.
func TransientAfter(err error, retryAfter time.Duration) error {
	return &TransientError{err: err, retryAfter: retryAfter}
}

func (e *TransientError) Error() string { return e.err.Error() }
func (e *TransientError) Unwrap() error { return e.err }

// Executor runs Operations with retry/backoff according to a Policy, using
// a Clock so delays are awaited (and testable) instead of hardcoded to
// time.Sleep.
type Executor struct {
	Policy Policy
	Clock  Clock
}

// NewExecutor returns an Executor using DefaultPolicy and the given Clock.
func NewExecutor(clock Clock) *Executor {
	return &Executor{Policy: DefaultPolicy(), Clock: clock}
}

// Operation is the unit of work an Executor retries. attempt is 0 for the
// first try and increments by one for each retry.
type Operation[T any] func(ctx context.Context, attempt int) (T, error)

// Do runs op, retrying on TransientError per exec's Policy. It returns as
// soon as op succeeds, a terminal (non-transient) error is returned, ctx is
// cancelled while waiting for backoff, or retries are exhausted.
func Do[T any](ctx context.Context, exec *Executor, op Operation[T]) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt <= exec.Policy.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := exec.Clock.Sleep(ctx, exec.delayFor(attempt, lastErr)); err != nil {
				return zero, err
			}
		}

		val, err := op(ctx, attempt)
		if err == nil {
			return val, nil
		}
		lastErr = err

		var transient *TransientError
		if !errors.As(err, &transient) {
			return zero, err
		}
	}
	return zero, lastErr
}

// delayFor computes the delay to wait before the given retry attempt
// (1-indexed). A Provider-supplied delay hint on lastErr overrides the
// computed exponential backoff, capped at MaxProviderDelay.
func (e *Executor) delayFor(attempt int, lastErr error) time.Duration {
	var transient *TransientError
	if errors.As(lastErr, &transient) && transient.retryAfter > 0 {
		if transient.retryAfter > e.Policy.MaxProviderDelay {
			return e.Policy.MaxProviderDelay
		}
		return transient.retryAfter
	}
	return BackoffDelay(attempt, e.Policy.BaseDelay, e.Policy.MaxDelay, e.Policy.Jitter)
}

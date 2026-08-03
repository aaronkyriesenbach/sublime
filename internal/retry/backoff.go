package retry

import (
	"math/rand"
	"time"
)

// BackoffDelay computes the exponential backoff delay before the given
// retry attempt (1-indexed: the first retry is attempt 1), doubling from
// base each attempt and capping at capDelay before jitter is applied.
//
// jitter receives the capped exponential delay and returns the actual delay
// to use; pass FullJitter for the project's default policy, or a
// deterministic stand-in in tests.
func BackoffDelay(attempt int, base, capDelay time.Duration, jitter func(max time.Duration) time.Duration) time.Duration {
	if attempt < 1 {
		return 0
	}

	shift := attempt - 1
	const maxShift = 32 // guards against overflow for pathological attempt values
	if shift > maxShift {
		shift = maxShift
	}

	exp := base * time.Duration(uint64(1)<<uint(shift))
	if exp > capDelay || exp < 0 {
		exp = capDelay
	}

	if jitter == nil {
		return exp
	}
	return jitter(exp)
}

// FullJitter implements the "full jitter" strategy: a uniformly random
// duration in [0, max]. It is the default Jitter function for Policy.
func FullJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}

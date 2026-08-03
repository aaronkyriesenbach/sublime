package retry

import (
	"testing"
	"time"
)

func TestBackoffDelay_ExponentialBeforeCap(t *testing.T) {
	noJitter := func(max time.Duration) time.Duration { return max }
	base := time.Second
	cap := 10 * time.Second

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
	}

	for _, c := range cases {
		got := BackoffDelay(c.attempt, base, cap, noJitter)
		if got != c.want {
			t.Errorf("BackoffDelay(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestBackoffDelay_ClampsShiftForPathologicalAttempt(t *testing.T) {
	noJitter := func(max time.Duration) time.Duration { return max }
	// A huge attempt count must not overflow; it should just cap at capDelay.
	got := BackoffDelay(1000, time.Second, 10*time.Second, noJitter)
	if got != 10*time.Second {
		t.Errorf("BackoffDelay(1000) = %v, want capped %v", got, 10*time.Second)
	}
}

func TestBackoffDelay_CapsAtMaxDelay(t *testing.T) {
	noJitter := func(max time.Duration) time.Duration { return max }
	base := time.Second
	cap := 10 * time.Second

	// attempt 5 -> 1s * 2^4 = 16s, which must be capped at 10s.
	got := BackoffDelay(5, base, cap, noJitter)
	if got != cap {
		t.Errorf("BackoffDelay(5) = %v, want capped %v", got, cap)
	}
}

func TestBackoffDelay_ZeroAttemptReturnsNoDelay(t *testing.T) {
	if got := BackoffDelay(0, time.Second, 10*time.Second, FullJitter); got != 0 {
		t.Errorf("BackoffDelay(0) = %v, want 0", got)
	}
}

func TestBackoffDelay_NilJitterReturnsExpDelay(t *testing.T) {
	got := BackoffDelay(2, time.Second, 10*time.Second, nil)
	if got != 2*time.Second {
		t.Errorf("BackoffDelay(2) with nil jitter = %v, want %v", got, 2*time.Second)
	}
}

func TestBackoffDelay_AppliesJitter(t *testing.T) {
	base := time.Second
	cap := 10 * time.Second
	called := false
	var gotMax time.Duration
	jitter := func(max time.Duration) time.Duration {
		called = true
		gotMax = max
		return 0
	}

	got := BackoffDelay(2, base, cap, jitter)

	if !called {
		t.Fatal("expected jitter function to be called")
	}
	if gotMax != 2*time.Second {
		t.Errorf("jitter called with max = %v, want %v", gotMax, 2*time.Second)
	}
	if got != 0 {
		t.Errorf("BackoffDelay = %v, want jittered value 0", got)
	}
}

func TestFullJitter_ZeroMaxReturnsZero(t *testing.T) {
	if got := FullJitter(0); got != 0 {
		t.Errorf("FullJitter(0) = %v, want 0", got)
	}
}

func TestFullJitter_WithinBounds(t *testing.T) {
	max := 10 * time.Second
	for i := 0; i < 1000; i++ {
		got := FullJitter(max)
		if got < 0 || got > max {
			t.Fatalf("FullJitter(%v) = %v, out of bounds [0, %v]", max, got, max)
		}
	}
}

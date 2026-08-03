package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRealClock_SleepReturnsAfterDuration(t *testing.T) {
	c := RealClock{}
	start := time.Now()
	if err := c.Sleep(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatalf("Sleep() error = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
		t.Errorf("Sleep returned after %v, want at least 5ms", elapsed)
	}
}

func TestRealClock_SleepZeroDurationReturnsImmediately(t *testing.T) {
	c := RealClock{}
	if err := c.Sleep(context.Background(), 0); err != nil {
		t.Fatalf("Sleep(0) error = %v, want nil", err)
	}
}

func TestRealClock_SleepReturnsContextErrOnCancel(t *testing.T) {
	c := RealClock{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Sleep(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep() error = %v, want %v", err, context.Canceled)
	}
}

func TestNewExecutor_UsesDefaultPolicy(t *testing.T) {
	clock := &fakeClock{}
	exec := NewExecutor(clock)

	if exec.Clock != clock {
		t.Error("NewExecutor did not use the given Clock")
	}
	want := DefaultPolicy()
	if exec.Policy.MaxRetries != want.MaxRetries ||
		exec.Policy.BaseDelay != want.BaseDelay ||
		exec.Policy.MaxDelay != want.MaxDelay ||
		exec.Policy.MaxProviderDelay != want.MaxProviderDelay {
		t.Errorf("NewExecutor policy = %+v, want %+v", exec.Policy, want)
	}
}

func TestTransientError_ErrorMessageMatchesWrappedError(t *testing.T) {
	inner := errors.New("503 service unavailable")
	err := Transient(inner)

	if err.Error() != inner.Error() {
		t.Errorf("Error() = %q, want %q", err.Error(), inner.Error())
	}
}

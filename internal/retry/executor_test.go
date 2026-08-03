package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock is a test double for Clock: it never actually sleeps, just
// records the delay it was asked to wait for.
type fakeClock struct {
	delays []time.Duration
	err    error // if set, Sleep returns this instead of nil
}

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.delays = append(f.delays, d)
	return f.err
}

func noJitter(max time.Duration) time.Duration { return max }

func testPolicy() Policy {
	p := DefaultPolicy()
	p.Jitter = noJitter // deterministic delays for assertions
	return p
}

func TestDo_TransientThenRecover(t *testing.T) {
	clock := &fakeClock{}
	exec := &Executor{Policy: testPolicy(), Clock: clock}

	attempts := 0
	op := func(_ context.Context, attempt int) (string, error) {
		attempts++
		if attempt < 2 {
			return "", Transient(errors.New("503 service unavailable"))
		}
		return "candidate", nil
	}

	got, err := Do(context.Background(), exec, op)

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if got != "candidate" {
		t.Errorf("Do() = %q, want %q", got, "candidate")
	}
	if attempts != 3 {
		t.Errorf("op called %d times, want 3 (initial + 2 retries)", attempts)
	}
	wantDelays := []time.Duration{1 * time.Second, 2 * time.Second}
	if len(clock.delays) != len(wantDelays) {
		t.Fatalf("recorded delays = %v, want %v", clock.delays, wantDelays)
	}
	for i, d := range wantDelays {
		if clock.delays[i] != d {
			t.Errorf("delay[%d] = %v, want %v", i, clock.delays[i], d)
		}
	}
}

func TestDo_TransientRetriesExhausted(t *testing.T) {
	clock := &fakeClock{}
	exec := &Executor{Policy: testPolicy(), Clock: clock}

	attempts := 0
	sentinel := errors.New("timeout")
	op := func(_ context.Context, _ int) (string, error) {
		attempts++
		return "", Transient(sentinel)
	}

	_, err := Do(context.Background(), exec, op)

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want wrapping %v", err, sentinel)
	}
	if attempts != 4 {
		t.Errorf("op called %d times, want 4 (initial + 3 retries)", attempts)
	}
	wantDelays := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	if len(clock.delays) != len(wantDelays) {
		t.Fatalf("recorded delays = %v, want %v", clock.delays, wantDelays)
	}
}

func TestDo_TerminalNoRetry(t *testing.T) {
	clock := &fakeClock{}
	exec := &Executor{Policy: testPolicy(), Clock: clock}

	attempts := 0
	terminal := errors.New("no candidate cleared cutoff")
	op := func(_ context.Context, _ int) (string, error) {
		attempts++
		return "", terminal
	}

	_, err := Do(context.Background(), exec, op)

	if !errors.Is(err, terminal) {
		t.Fatalf("Do() error = %v, want %v", err, terminal)
	}
	if attempts != 1 {
		t.Errorf("op called %d times, want 1 (no retry on terminal error)", attempts)
	}
	if len(clock.delays) != 0 {
		t.Errorf("recorded delays = %v, want none", clock.delays)
	}
}

func TestDo_ProviderDelayHintOverridesComputedDelay(t *testing.T) {
	clock := &fakeClock{}
	exec := &Executor{Policy: testPolicy(), Clock: clock}

	attempts := 0
	op := func(_ context.Context, attempt int) (string, error) {
		attempts++
		if attempt < 1 {
			return "", TransientAfter(errors.New("429 too many requests"), 30*time.Second)
		}
		return "candidate", nil
	}

	_, err := Do(context.Background(), exec, op)

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if len(clock.delays) != 1 || clock.delays[0] != 30*time.Second {
		t.Errorf("recorded delays = %v, want [30s] (provider hint, not computed backoff)", clock.delays)
	}
}

func TestDo_ProviderDelayHintCappedAtMaxProviderDelay(t *testing.T) {
	clock := &fakeClock{}
	exec := &Executor{Policy: testPolicy(), Clock: clock}

	op := func(_ context.Context, attempt int) (string, error) {
		if attempt < 1 {
			return "", TransientAfter(errors.New("429 too many requests"), 5*time.Minute)
		}
		return "candidate", nil
	}

	_, err := Do(context.Background(), exec, op)

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if len(clock.delays) != 1 || clock.delays[0] != exec.Policy.MaxProviderDelay {
		t.Errorf("recorded delays = %v, want [%v] (capped provider hint)", clock.delays, exec.Policy.MaxProviderDelay)
	}
}

func TestDo_ContextCancelledDuringBackoffStopsRetrying(t *testing.T) {
	clock := &fakeClock{err: context.Canceled}
	exec := &Executor{Policy: testPolicy(), Clock: clock}

	attempts := 0
	op := func(_ context.Context, _ int) (string, error) {
		attempts++
		return "", Transient(errors.New("timeout"))
	}

	_, err := Do(context.Background(), exec, op)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want %v", err, context.Canceled)
	}
	if attempts != 1 {
		t.Errorf("op called %d times, want 1 (stopped after cancelled sleep)", attempts)
	}
}

func TestDefaultPolicy_MatchesAgreedParameters(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", p.MaxRetries)
	}
	if p.BaseDelay != time.Second {
		t.Errorf("BaseDelay = %v, want 1s", p.BaseDelay)
	}
	if p.MaxDelay != 10*time.Second {
		t.Errorf("MaxDelay = %v, want 10s", p.MaxDelay)
	}
	if p.MaxProviderDelay != 60*time.Second {
		t.Errorf("MaxProviderDelay = %v, want 60s", p.MaxProviderDelay)
	}
}

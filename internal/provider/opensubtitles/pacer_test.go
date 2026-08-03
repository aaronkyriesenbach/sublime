package opensubtitles

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock is a test double for retry.Clock: it never actually sleeps,
// just records the delay it was asked to wait for, mirroring the
// internal/retry package's own test convention.
type fakeClock struct {
	delays []time.Duration
	err    error
}

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.delays = append(f.delays, d)
	return f.err
}

func testPacerConfig() pacerConfig {
	return pacerConfig{Pace: time.Second, MaxDelay: 60 * time.Second, DecayThreshold: 10}
}

func cleanOutcome() outcome     { return outcome{responded: true} }
func retryableOutcome() outcome { return outcome{responded: true, retryable: true} }

func TestPacer_FirstDispatchDoesNotWait(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	err := p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return cleanOutcome(), nil
	})
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if len(clock.delays) != 0 {
		t.Errorf("delays = %v, want none before the first dispatch", clock.delays)
	}
}

func TestPacer_SubsequentDispatchWaitsThePace(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	call := func() error {
		return p.do(context.Background(), func(_ context.Context) (outcome, error) {
			return cleanOutcome(), nil
		})
	}
	if err := call(); err != nil {
		t.Fatalf("first do() error = %v", err)
	}
	if err := call(); err != nil {
		t.Fatalf("second do() error = %v", err)
	}

	if len(clock.delays) != 1 || clock.delays[0] != time.Second {
		t.Errorf("delays = %v, want [1s] before the second dispatch", clock.delays)
	}
}

func TestPacer_WidensOnRetryableResponse(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	// First dispatch: a 429/5xx, which should double the delay applied
	// before the next one.
	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return retryableOutcome(), errors.New("429")
	})
	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return cleanOutcome(), nil
	})

	if len(clock.delays) != 1 || clock.delays[0] != 2*time.Second {
		t.Errorf("delays = %v, want [2s] after one retryable response", clock.delays)
	}
}

func TestPacer_WidenDoublesRepeatedlyAndCapsAtMaxDelay(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	dispatch := func(oc outcome) {
		_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
			return oc, nil
		})
	}

	dispatch(cleanOutcome()) // 1st: no wait, establishes dispatched=true
	for i := 0; i < 10; i++ {
		dispatch(retryableOutcome()) // widen repeatedly: 1->2->4->...->60 (capped)
	}

	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second,
	}
	if len(clock.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", clock.delays, want)
	}
	for i, d := range want {
		if clock.delays[i] != d {
			t.Errorf("delay[%d] = %v, want %v", i, clock.delays[i], d)
		}
	}
}

func TestPacer_AdoptsProviderDelayHintDirectlyInsteadOfDoubling(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return cleanOutcome(), nil
	})
	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return outcome{responded: true, retryable: true, delayHint: 45 * time.Second}, nil
	})
	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return cleanOutcome(), nil
	})

	want := []time.Duration{1 * time.Second, 45 * time.Second}
	if len(clock.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", clock.delays, want)
	}
	for i, d := range want {
		if clock.delays[i] != d {
			t.Errorf("delay[%d] = %v, want %v", i, clock.delays[i], d)
		}
	}
}

func TestPacer_DelayHintIsCappedAtMaxDelay(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return cleanOutcome(), nil
	})
	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return outcome{responded: true, retryable: true, delayHint: 5 * time.Minute}, nil
	})
	_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return cleanOutcome(), nil
	})

	want := []time.Duration{1 * time.Second, 60 * time.Second}
	if len(clock.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", clock.delays, want)
	}
	for i, d := range want {
		if clock.delays[i] != d {
			t.Errorf("delay[%d] = %v, want %v", i, clock.delays[i], d)
		}
	}
}

func TestPacer_DecaysAfterTenConsecutiveCleanResponses(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	dispatch := func(oc outcome) {
		_ = p.do(context.Background(), func(_ context.Context) (outcome, error) {
			return oc, nil
		})
	}

	dispatch(cleanOutcome())     // 1st: establishes dispatched=true, delay stays at Pace (1s)
	dispatch(retryableOutcome()) // widen to 2s
	for i := 0; i < 10; i++ {
		dispatch(cleanOutcome()) // 10 consecutive clean responses: halve back to 1s
	}
	dispatch(cleanOutcome()) // 11th dispatch: delay should already be back at the 1s floor

	want := []time.Duration{
		1 * time.Second, // before dispatch 2 (retryable)
		2 * time.Second, // before dispatch 3 (1st clean)
		2 * time.Second, // dispatch 4
		2 * time.Second, // dispatch 5
		2 * time.Second, // dispatch 6
		2 * time.Second, // dispatch 7
		2 * time.Second, // dispatch 8
		2 * time.Second, // dispatch 9
		2 * time.Second, // dispatch 10
		2 * time.Second, // dispatch 11
		2 * time.Second, // dispatch 12 (10th clean response since the widen; decays to 1s after this wait)
		1 * time.Second, // dispatch 13: delay is back at the floor
	}
	if len(clock.delays) != len(want) {
		t.Fatalf("got %d delays %v, want %d %v", len(clock.delays), clock.delays, len(want), want)
	}
	for i, d := range want {
		if clock.delays[i] != d {
			t.Errorf("delay[%d] = %v, want %v", i, clock.delays[i], d)
		}
	}
}

func TestPacer_DoesNotUpdateStateWhenNoResponseWasReceived(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	dispatch := func(oc outcome, err error) error {
		return p.do(context.Background(), func(_ context.Context) (outcome, error) {
			return oc, err
		})
	}

	_ = dispatch(cleanOutcome(), nil)
	transportErr := errors.New("connection refused")
	if err := dispatch(outcome{}, transportErr); !errors.Is(err, transportErr) {
		t.Fatalf("do() error = %v, want %v", err, transportErr)
	}
	_ = dispatch(cleanOutcome(), nil)

	// The transport failure carried outcome{} (responded: false) and must
	// not have nudged the clean streak or delay: the pace stays at 1s.
	want := []time.Duration{1 * time.Second, 1 * time.Second}
	if len(clock.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", clock.delays, want)
	}
}

func TestPacer_PropagatesErrorFromCallback(t *testing.T) {
	clock := &fakeClock{}
	p := newPacer(testPacerConfig(), clock)

	sentinel := errors.New("boom")
	err := p.do(context.Background(), func(_ context.Context) (outcome, error) {
		return retryableOutcome(), sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("do() error = %v, want %v", err, sentinel)
	}
}

func TestPacer_StopsWaitingWhenClockErrors(t *testing.T) {
	clock := &fakeClock{err: context.Canceled}
	p := newPacer(testPacerConfig(), clock)

	calls := 0
	dispatch := func() error {
		return p.do(context.Background(), func(_ context.Context) (outcome, error) {
			calls++
			return cleanOutcome(), nil
		})
	}
	if err := dispatch(); err != nil {
		t.Fatalf("first do() error = %v", err)
	}
	if err := dispatch(); !errors.Is(err, context.Canceled) {
		t.Fatalf("second do() error = %v, want %v", err, context.Canceled)
	}
	if calls != 1 {
		t.Errorf("callback invoked %d times, want 1 (second call should stop at the cancelled sleep)", calls)
	}
}

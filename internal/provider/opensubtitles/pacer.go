package opensubtitles

import (
	"context"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// pacerConfig holds pacer's tunable parameters, agreed in issue #25's
// resolution: a 1 req/sec default steady-state pace, a 60s cap on the
// adaptively-widened delay (shared with retry.Policy.MaxProviderDelay so
// the queue never self-imposes a stricter pace than the per-task retry
// engine would clip a Provider delay hint to), and a 10-clean-response
// decay threshold.
type pacerConfig struct {
	Pace           time.Duration
	MaxDelay       time.Duration
	DecayThreshold int
}

// outcome reports how a dispatched request resolved, for pacer's own
// adaptive-backoff bookkeeping — independent of whatever error the
// request's caller (e.g. retry.Executor) ultimately sees. Responded is
// false for a transport-level failure (no HTTP response was received at
// all), which leaves the pacer's delay and clean streak untouched rather
// than guessing at a status that was never observed.
type outcome struct {
	responded bool
	retryable bool          // true on a 429/5xx response
	delayHint time.Duration // a Provider-supplied delay hint (e.g. Retry-After), zero if absent
}

// pacer serializes outgoing requests through a worker pool of one and
// layers queue-level adaptive backoff on top of — not instead of — the
// per-task retry.Executor (issue #25's resolution): every request behind a
// 429/5xx in the queue is paced more conservatively, not just the one that
// got flagged, since a 429 is evidence the whole Provider is currently hot.
type pacer struct {
	cfg   pacerConfig
	clock retry.Clock

	// mu is the worker pool of one: holding it for the full wait+dispatch
	// duration is what makes dispatch serial.
	mu          chan struct{}
	dispatched  bool
	delay       time.Duration
	cleanStreak int
}

func newPacer(cfg pacerConfig, clock retry.Clock) *pacer {
	mu := make(chan struct{}, 1)
	mu <- struct{}{}
	return &pacer{cfg: cfg, clock: clock, mu: mu, delay: cfg.Pace}
}

// do serializes send behind the pacer's single worker slot, waiting out
// the current effective delay since the last dispatch (skipped for the
// very first dispatch, which has nothing to pace against), then folds the
// outcome it reports into the pacer's adaptive state before returning
// send's error unchanged.
func (p *pacer) do(ctx context.Context, send func(ctx context.Context) (outcome, error)) error {
	select {
	case <-p.mu:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { p.mu <- struct{}{} }()

	if p.dispatched {
		if err := p.clock.Sleep(ctx, p.delay); err != nil {
			return err
		}
	}
	p.dispatched = true

	out, err := send(ctx)
	if out.responded {
		p.observe(out)
	}
	return err
}

// observe updates delay and cleanStreak from a completed dispatch's
// outcome: a Provider-supplied delay hint is adopted directly (a stronger
// signal than a blind guess); otherwise a retryable (429/5xx) response
// doubles the delay, capped at MaxDelay; a clean response counts toward
// DecayThreshold consecutive clean responses, at which point the delay is
// halved back toward Pace and the streak resets, repeating until it
// reaches the floor.
func (p *pacer) observe(out outcome) {
	switch {
	case out.delayHint > 0:
		p.delay = min(out.delayHint, p.cfg.MaxDelay)
		p.cleanStreak = 0
	case out.retryable:
		widened := p.delay * 2
		if widened > p.cfg.MaxDelay || widened < p.delay {
			widened = p.cfg.MaxDelay
		}
		p.delay = widened
		p.cleanStreak = 0
	default:
		p.cleanStreak++
		if p.cleanStreak >= p.cfg.DecayThreshold && p.delay > p.cfg.Pace {
			p.delay = max(p.delay/2, p.cfg.Pace)
			p.cleanStreak = 0
		}
	}
}

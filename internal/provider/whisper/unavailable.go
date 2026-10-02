package whisper

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// unavailableResumeAfter is how long an Unavailable Suspension lasts. It is
// deliberately short: a sidecar restart is usually over in about this long,
// and a still-down sidecar just re-suspends on the next attempt.
const unavailableResumeAfter = 2 * time.Minute

// Suspension reports whether the Provider is Suspended because the sidecar
// was unreachable, and when it will try again. It implements the optional
// suspension-reporting capability the dispatcher and GET /status use. A
// resumeAt that has passed reads as not suspended; the Suspension itself is
// only cleared once a request is answered.
func (p *Provider) Suspension() (resumeAt time.Time, suspended bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.suspendedUntil.IsZero() || !p.now().Before(p.suspendedUntil) {
		return time.Time{}, false
	}
	return p.suspendedUntil, true
}

// suspendedError short-circuits a call with the Unavailable signal while the
// Suspension lasts, so the sidecar is not contacted before resume time.
func (p *Provider) suspendedError() error {
	if resumeAt, suspended := p.Suspension(); suspended {
		return &provider.UnavailableError{ResumeAt: resumeAt}
	}
	return nil
}

// transcribe sends audio to the sidecar for transcription, giving each
// attempt at most timeout; see requestSidecar for how failures are handled.
func (p *Provider) transcribe(ctx context.Context, audio []byte, languageCode string, temperature float64, timeout time.Duration) ([]segment, error) {
	return requestSidecar(ctx, p, func(ctx context.Context) ([]segment, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return p.client.transcribe(ctx, audio, languageCode, temperature)
	})
}

// detectSidecarLanguage sends a clip to the sidecar for language detection,
// giving each attempt at most detectionTimeout; see requestSidecar for how failures are handled.
func (p *Provider) detectSidecarLanguage(ctx context.Context, clip []byte) (string, error) {
	return requestSidecar(ctx, p, func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, p.detectionTimeout)
		defer cancel()
		return p.client.detectLanguage(ctx, clip)
	})
}

// requestSidecar runs one sidecar request. A reachable sidecar's errors are
// retried with backoff (5xx only) and otherwise returned as ordinary
// request failures; an unreachable sidecar enters an Unavailable
// Suspension and returns a provider.UnavailableError.
func requestSidecar[T any](ctx context.Context, p *Provider, send func(context.Context) (T, error)) (T, error) {
	result, err := retry.Do(ctx, p.executor, func(ctx context.Context, _ int) (T, error) {
		return send(ctx)
	})

	var answered *sidecarError
	switch {
	case err == nil || errors.As(err, &answered):
		p.recoverFromOutage()
	case ctx.Err() == nil && isConnectFailure(err):
		var zero T
		return zero, p.enterUnavailable(err)
	}
	return result, err
}

// isConnectFailure reports whether err means no connection to the sidecar
// could be established (refused, unreachable, unresolvable, connect
// timeout). A connection lost mid-request is an ordinary failure instead:
// the sidecar was reachable, and a follow-up attempt finds out whether it
// still is.
func isConnectFailure(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

func (p *Provider) enterUnavailable(cause error) error {
	resumeAt := p.now().Add(unavailableResumeAfter)

	p.mu.Lock()
	p.suspendedUntil = resumeAt
	p.unavailable = true
	p.mu.Unlock()

	p.log.Warn("whisper: sidecar unreachable; suspending as unavailable", "cause", cause, "resume_at", resumeAt)
	return &provider.UnavailableError{ResumeAt: resumeAt, Cause: cause}
}

func (p *Provider) recoverFromOutage() {
	p.mu.Lock()
	wasUnavailable := p.unavailable
	p.suspendedUntil = time.Time{}
	p.unavailable = false
	p.mu.Unlock()

	if wasUnavailable {
		p.log.Info("whisper: sidecar reachable again; leaving unavailable suspension")
	}
}

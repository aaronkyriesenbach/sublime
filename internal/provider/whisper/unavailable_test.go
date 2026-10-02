package whisper_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/whisper"
	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// recordingClock is a retry.Clock that records the backoff delays asked of
// it instead of sleeping.
type recordingClock struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (c *recordingClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	return nil
}

func (c *recordingClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

var _ retry.Clock = (*recordingClock)(nil)

// switchableTransport refuses connections while down, like a stopped
// sidecar, and otherwise forwards to the real transport.
type switchableTransport struct {
	down atomic.Bool
}

func (s *switchableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.down.Load() {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.OpError{Op: "connect", Err: syscall.ECONNREFUSED}}
	}
	return http.DefaultTransport.RoundTrip(req)
}

// outageFixture is a whisper Provider whose sidecar can be taken down and
// brought back, with a controllable clock and captured logs.
type outageFixture struct {
	provider  *whisper.Provider
	sidecar   *fakeWhisperServer
	transport *switchableTransport
	logs      *bytes.Buffer
	now       *time.Time
	candidate domain.Candidate
}

func newOutageFixture(t *testing.T) *outageFixture {
	t.Helper()
	f := &outageFixture{
		sidecar:   newFakeWhisperServer(t, sampleResponse(t)),
		transport: &switchableTransport{},
		logs:      &bytes.Buffer{},
		now:       new(time.Time),
	}
	*f.now = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	p, err := whisper.New(whisper.Config{
		Endpoint:   f.sidecar.URL,
		Audio:      &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}},
		HTTPClient: &http.Client{Transport: f.transport},
		Logger:     slog.New(slog.NewTextHandler(f.logs, nil)),
		Clock:      &recordingClock{},
		Now:        func() time.Time { return *f.now },
	})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}
	f.provider = p

	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	f.candidate = candidates[0]
	return f
}

func TestDownload_UnreachableSidecarSuspendsAsUnavailable(t *testing.T) {
	f := newOutageFixture(t)
	f.transport.down.Store(true)

	_, err := f.provider.Download(context.Background(), f.candidate)

	var unavailable *provider.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Download error = %v, want a *provider.UnavailableError", err)
	}
	wantResume := f.now.Add(2 * time.Minute)
	if !unavailable.ResumeAt.Equal(wantResume) {
		t.Errorf("ResumeAt = %v, want %v (default resume time)", unavailable.ResumeAt, wantResume)
	}
	resumeAt, suspended := f.provider.Suspension()
	if !suspended || !resumeAt.Equal(wantResume) {
		t.Errorf("Suspension() = (%v, %v), want (%v, true)", resumeAt, suspended, wantResume)
	}
	if !bytes.Contains(f.logs.Bytes(), []byte("suspending as unavailable")) {
		t.Errorf("entering Suspension was not logged:\n%s", f.logs)
	}
}

func TestDownload_ConnectionRefusedByAStoppedSidecarSuspendsAsUnavailable(t *testing.T) {
	sidecar := newFakeWhisperServer(t, sampleResponse(t))
	p, err := whisper.New(whisper.Config{
		Endpoint: sidecar.URL,
		Audio:    &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}},
		Clock:    &recordingClock{},
	})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}
	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}
	sidecar.Close()

	_, err = p.Download(context.Background(), candidates[0])

	var unavailable *provider.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Download error = %v, want a *provider.UnavailableError", err)
	}
}

func TestSuspension_BlocksCallsUntilResumeTime(t *testing.T) {
	f := newOutageFixture(t)
	f.transport.down.Store(true)
	_, _ = f.provider.Download(context.Background(), f.candidate)
	f.transport.down.Store(false)

	var unavailable *provider.UnavailableError
	if _, err := f.provider.Search(context.Background(), query(language.English)); !errors.As(err, &unavailable) {
		t.Errorf("Search while suspended error = %v, want *provider.UnavailableError", err)
	}
	if _, err := f.provider.Download(context.Background(), f.candidate); !errors.As(err, &unavailable) {
		t.Errorf("Download while suspended error = %v, want *provider.UnavailableError", err)
	}
	if got := len(f.sidecar.Requests()); got != 0 {
		t.Errorf("sidecar saw %d requests during the Suspension, want 0", got)
	}

	*f.now = f.now.Add(2 * time.Minute)
	if _, suspended := f.provider.Suspension(); suspended {
		t.Error("Suspension() = suspended after the resume time passed, want not suspended")
	}
}

func TestSuspension_ClearsAndLogsWhenTheSidecarAnswersAgain(t *testing.T) {
	f := newOutageFixture(t)
	f.transport.down.Store(true)
	_, _ = f.provider.Download(context.Background(), f.candidate)
	f.transport.down.Store(false)
	*f.now = f.now.Add(3 * time.Minute)

	if _, err := f.provider.Download(context.Background(), f.candidate); err != nil {
		t.Fatalf("Download after recovery: %v", err)
	}

	if _, suspended := f.provider.Suspension(); suspended {
		t.Error("Suspension() = suspended after a successful request, want cleared")
	}
	if !bytes.Contains(f.logs.Bytes(), []byte("leaving unavailable suspension")) {
		t.Errorf("leaving Suspension was not logged:\n%s", f.logs)
	}
}

func TestSuspension_StillDownAtResumeTimeSuspendsAgain(t *testing.T) {
	f := newOutageFixture(t)
	f.transport.down.Store(true)
	_, _ = f.provider.Download(context.Background(), f.candidate)
	*f.now = f.now.Add(3 * time.Minute)

	_, err := f.provider.Download(context.Background(), f.candidate)

	var unavailable *provider.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Download error = %v, want *provider.UnavailableError", err)
	}
	if _, suspended := f.provider.Suspension(); !suspended {
		t.Error("Suspension() = not suspended after another failed attempt, want suspended")
	}
}

func TestDownload_CancelledRequestDoesNotSuspend(t *testing.T) {
	f := newOutageFixture(t)
	f.transport.down.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.provider.Download(ctx, f.candidate)

	var unavailable *provider.UnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("Download error = %v, want a cancellation, not an outage", err)
	}
	if _, suspended := f.provider.Suspension(); suspended {
		t.Error("Suspension() = suspended after a cancelled request, want not suspended")
	}
}

func TestDownload_ReachableSidecarReturning5xxIsRetriedThenFailsWithoutSuspending(t *testing.T) {
	sidecar := newFakeWhisperServer(t, []byte(`{"error":"failed to process audio"}`))
	sidecar.status = http.StatusInternalServerError
	clock := &recordingClock{}
	p, err := whisper.New(whisper.Config{
		Endpoint: sidecar.URL,
		Audio:    &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}},
		Clock:    clock,
	})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}
	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}

	_, err = p.Download(context.Background(), candidates[0])

	if err == nil {
		t.Fatal("Download: want an error for a persistent 5xx, got nil")
	}
	var unavailable *provider.UnavailableError
	if errors.As(err, &unavailable) {
		t.Errorf("Download error = %v: a reachable sidecar's errors are not an outage", err)
	}
	if _, suspended := p.Suspension(); suspended {
		t.Error("Suspension() = suspended by a reachable sidecar's 5xx, want not suspended")
	}
	if got := len(sidecar.Requests()); got != 4 {
		t.Errorf("sidecar saw %d requests, want 4 (first try plus 3 retries)", got)
	}
	if got := len(clock.Sleeps()); got != 3 {
		t.Errorf("backed off %d times, want 3", got)
	}
}

func TestDownload_ClientErrorFromReachableSidecarIsNotRetried(t *testing.T) {
	sidecar := newFakeWhisperServer(t, []byte(`{"error":"bad request"}`))
	sidecar.status = http.StatusBadRequest
	p := newProvider(t, sidecar, &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}, nil)
	candidates, err := p.Search(context.Background(), query(language.English))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Search = %+v, %v; want exactly one candidate", candidates, err)
	}

	if _, err := p.Download(context.Background(), candidates[0]); err == nil {
		t.Fatal("Download: want an error for a 400, got nil")
	}
	if got := len(sidecar.Requests()); got != 1 {
		t.Errorf("sidecar saw %d requests, want 1 (no retry for a 4xx)", got)
	}
}

package whisper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
)

// The timeout is a Provider field, not configuration, so this test lives
// in the package to shorten it.
func TestSearch_WedgedSidecarFailsLanguageDetectionAtTheTimeout(t *testing.T) {
	// The server only notices a vanished client once the request body is
	// read, and the done channel guarantees the handler can never outlive
	// the test.
	done := make(chan struct{})
	sidecar := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	t.Cleanup(func() {
		close(done)
		sidecar.Close()
	})
	p, err := New(Config{
		Endpoint:    sidecar.URL,
		ChunkLength: time.Minute,
		Audio: &audiosource.FakeSource{
			Streams:       []audiosource.Stream{{Index: 1}},
			VideoDuration: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.detectionTimeout = 50 * time.Millisecond

	_, err = p.Search(context.Background(), provider.Query{Path: "/media/a.mkv", Language: language.English})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Search error = %v, want the detection request to time out", err)
	}
	var unavailable *provider.UnavailableError
	if errors.As(err, &unavailable) {
		t.Errorf("Search error = %v: a wedged sidecar is not an outage", err)
	}
}

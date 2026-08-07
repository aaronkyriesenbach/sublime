package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/api"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

type reprocessResponse = api.ReprocessResponse

// recordingReprocessor is a test double for api.ReprocessFunc that records
// every call and blocks until released, so tests can assert the HTTP
// response returns before the background work finishes.
type recordingReprocessor struct {
	mu      sync.Mutex
	calls   []recordedCall
	release chan struct{}
	err     error
}

type recordedCall struct {
	Library domain.Library
	Target  string
}

func newRecordingReprocessor() *recordingReprocessor {
	return &recordingReprocessor{release: make(chan struct{})}
}

func (r *recordingReprocessor) Reprocess(ctx context.Context, lib domain.Library, target string) error {
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{Library: lib, Target: target})
	err := r.err
	r.mu.Unlock()

	select {
	case <-r.release:
	case <-ctx.Done():
	}
	return err
}

func (r *recordingReprocessor) Calls() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedCall(nil), r.calls...)
}

func TestReprocess_AcceptsAValidPathAndReturns202Immediately(t *testing.T) {
	rec := newRecordingReprocessor()
	defer close(rec.release)

	libs := []domain.Library{{Name: "movies", Path: "/media/movies"}}
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: libs, Reprocess: rec.Reprocess})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"path": "/media/movies/a.mkv"})
	resp, err := http.Post(ts.URL+"/reprocess", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /reprocess: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var decoded reprocessResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !decoded.Accepted {
		t.Error("accepted = false, want true")
	}
	if decoded.Scope.Path != "/media/movies/a.mkv" {
		t.Errorf("scope.path = %q, want /media/movies/a.mkv", decoded.Scope.Path)
	}
}

func TestReprocess_RunsAsynchronouslyInTheBackground(t *testing.T) {
	rec := newRecordingReprocessor()

	libs := []domain.Library{{Name: "movies", Path: "/media/movies"}}
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: libs, Reprocess: rec.Reprocess})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"path": "/media/movies/a.mkv"})
	resp, err := http.Post(ts.URL+"/reprocess", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /reprocess: %v", err)
	}
	_ = resp.Body.Close()

	// The reprocessor blocks on rec.release, so if the HTTP handler had
	// waited for it synchronously, this Post would still be blocked above.
	// Reaching here proves the call returned before the work finished.
	deadline := time.After(time.Second)
	for len(rec.Calls()) != 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the background reprocess call to be recorded")
		case <-time.After(time.Millisecond):
		}
	}

	close(rec.release)
	srv.Close() // waits for the background goroutine to finish
}

func TestReprocess_UnknownPathReturns404(t *testing.T) {
	libs := []domain.Library{{Name: "movies", Path: "/media/movies"}}
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: libs, Reprocess: newRecordingReprocessor().Reprocess})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"path": "/not/a/library/path"})
	resp, err := http.Post(ts.URL+"/reprocess", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /reprocess: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestReprocess_MissingPathReturns400(t *testing.T) {
	libs := []domain.Library{{Name: "movies", Path: "/media/movies"}}
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: libs, Reprocess: newRecordingReprocessor().Reprocess})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/reprocess", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("POST /reprocess: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestReprocess_MalformedBodyReturns400(t *testing.T) {
	libs := []domain.Library{{Name: "movies", Path: "/media/movies"}}
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: libs, Reprocess: newRecordingReprocessor().Reprocess})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/reprocess", "application/json", bytes.NewReader([]byte(`not json`)))
	if err != nil {
		t.Fatalf("POST /reprocess: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestReprocess_RejectsNonPOST(t *testing.T) {
	libs := []domain.Library{{Name: "movies", Path: "/media/movies"}}
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: libs, Reprocess: newRecordingReprocessor().Reprocess})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/reprocess")
	if err != nil {
		t.Fatalf("GET /reprocess: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

package opensubtitles_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordedRequest captures what the mock server observed for one request,
// for tests that assert on headers/body/query rather than just the
// response the fake returns.
type recordedRequest struct {
	Method  string
	Path    string
	Query   string
	Headers http.Header
	Body    []byte
}

// mockServer is a small, scriptable stand-in for the OpenSubtitles API,
// used instead of live API calls per the ticket's testing requirement. It
// records every request it receives and dispatches to a per-path handler
// queue: each call to a given "METHOD PATH" pops its next configured
// response, so a test can script a 429 followed by a 200 for the same
// endpoint (retry/backoff scenarios).
type mockServer struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	queues   map[string][]func(w http.ResponseWriter, r *http.Request)
}

func newMockServer(t *testing.T) *mockServer {
	t.Helper()
	m := &mockServer{t: t, queues: make(map[string][]func(w http.ResponseWriter, r *http.Request))}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockServer) URL() string { return m.server.URL }

// on enqueues handler as the next response for method+path (query string
// excluded from the key), in the order tests register them.
func (m *mockServer) on(method, path string, handler func(w http.ResponseWriter, r *http.Request)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := method + " " + path
	m.queues[key] = append(m.queues[key], handler)
}

func (m *mockServer) handle(w http.ResponseWriter, r *http.Request) {
	body := readBody(m.t, r)

	m.mu.Lock()
	m.requests = append(m.requests, recordedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: r.Header.Clone(),
		Body:    body,
	})

	key := r.Method + " " + r.URL.Path
	queue := m.queues[key]
	if len(queue) == 0 {
		m.mu.Unlock()
		m.t.Fatalf("mockServer: no handler queued for %s (recorded %d requests so far)", key, len(m.requests))
		return
	}
	next := queue[0]
	m.queues[key] = queue[1:]
	m.mu.Unlock()

	next(w, r)
}

func (m *mockServer) requestsFor(method, path string) []recordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []recordedRequest
	for _, req := range m.requests {
		if req.Method == method && req.Path == path {
			out = append(out, req)
		}
	}
	return out
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	if r.Body == nil {
		return nil
	}
	defer func() { _ = r.Body.Close() }()
	data := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			break
		}
	}
	return data
}

// jsonHandler returns a handler writing status and body (marshaled to
// JSON) as the response.
func jsonHandler(status int, body any) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		encoded, err := json.Marshal(body)
		if err != nil {
			panic(fmt.Sprintf("mockServer: encoding response body: %v", err))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(encoded)
	}
}

// rawHandler returns a handler writing status and a raw (non-JSON) body,
// for the signed-URL download step.
func rawHandler(status int, body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// withRetryAfter wraps handler to also set a Retry-After header.
func withRetryAfter(seconds int, handler func(w http.ResponseWriter, r *http.Request)) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
		handler(w, r)
	}
}

func bearerToken(t *testing.T, req recordedRequest) string {
	t.Helper()
	auth := req.Headers.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

package subdl_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// recordedRequest captures what the mock server observed for one request,
// for tests that assert on headers/query rather than just the response the
// fake returns.
type recordedRequest struct {
	Method  string
	Path    string
	Query   string
	Headers http.Header
}

// mockServer is a small, scriptable stand-in for the SubDL API, mirroring
// opensubtitles' own mockServer test double (opensubtitles/mockserver_test.go).
// It records every request it receives and dispatches to a per-path handler
// queue: each call to a given "METHOD PATH" pops its next configured
// response.
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
	m.mu.Lock()
	m.requests = append(m.requests, recordedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: r.Header.Clone(),
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
// for the subtitle download endpoint.
func rawHandler(status int, body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

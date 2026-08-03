package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/api"
)

func TestHealth_ReturnsOKWithNoDependencyChecks(t *testing.T) {
	srv := api.NewServer(api.Deps{})
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHealth_RejectsNonGET(t *testing.T) {
	srv := api.NewServer(api.Deps{})
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/health", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

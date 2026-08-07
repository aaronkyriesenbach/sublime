package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/api"
	"github.com/aaronkyriesenbach/sublime/internal/cli"
)

func TestStatusCommand_UnscopedPrintsLibrarySummaryTable(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("library") != "" || r.URL.Query().Get("path") != "" {
			t.Errorf("unexpected scoping query params: %v", r.URL.Query())
		}
		resp := api.StatusResponse{Libraries: []api.LibrarySummaryEntry{
			{Name: "movies", Pending: 1, Synced: 2, Failed: 3},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"status", "--api", apiURL})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := out.String()
	for _, want := range []string{"movies", "1", "2", "3"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q; got:\n%s", want, output)
		}
	}
}

func TestStatusCommand_ScopedByLibraryPassesQueryParamAndPrintsFiles(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("library"); got != "movies" {
			t.Errorf("library query param = %q, want movies", got)
		}
		total, limit, offset := 1, 100, 0
		resp := api.StatusResponse{
			Libraries: []api.LibrarySummaryEntry{{Name: "movies", Failed: 1}},
			Files: []api.FileEntry{
				{
					Path:        "/media/movies/a.mkv",
					Library:     "movies",
					ContentHash: "abc123",
					Languages: map[string]api.LanguageStateEntry{
						"en": {Status: "failed", Reason: "no_candidate"},
					},
				},
			},
			Total: &total, Limit: &limit, Offset: &offset,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"status", "--library", "movies", "--api", apiURL})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := out.String()
	for _, want := range []string{"/media/movies/a.mkv", "en", "failed", "no_candidate"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q; got:\n%s", want, output)
		}
	}
}

func TestStatusCommand_PrintsAttemptedProvidersColumn(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		total, limit, offset := 1, 100, 0
		resp := api.StatusResponse{
			Libraries: []api.LibrarySummaryEntry{{Name: "movies", Pending: 1}},
			Files: []api.FileEntry{
				{
					Path:        "/media/movies/a.mkv",
					Library:     "movies",
					ContentHash: "abc123",
					Languages: map[string]api.LanguageStateEntry{
						"en": {Status: "pending", Attempted: []string{"opensubtitles", "subdl"}},
					},
				},
			},
			Total: &total, Limit: &limit, Offset: &offset,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"status", "--library", "movies", "--api", apiURL})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := out.String()
	for _, want := range []string{"ATTEMPTED", "opensubtitles,subdl"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q; got:\n%s", want, output)
		}
	}
}

func TestStatusCommand_PassesPathStateLimitOffsetQueryParams(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("path") != "/media/movies/a.mkv" {
			t.Errorf("path = %q", q.Get("path"))
		}
		if q.Get("state") != "all" {
			t.Errorf("state = %q, want all", q.Get("state"))
		}
		if q.Get("limit") != "50" {
			t.Errorf("limit = %q, want 50", q.Get("limit"))
		}
		if q.Get("offset") != "10" {
			t.Errorf("offset = %q, want 10", q.Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.StatusResponse{})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{
		"status", "--path", "/media/movies/a.mkv", "--state", "all",
		"--limit", "50", "--offset", "10", "--api", apiURL,
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
}

func TestStatusCommand_JSONFlagPrintsRawAPIResponse(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.StatusResponse{
			Libraries: []api.LibrarySummaryEntry{{Name: "movies"}},
		})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"status", "--api", apiURL, "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var decoded api.StatusResponse
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\noutput:\n%s", err, out.String())
	}
	if len(decoded.Libraries) != 1 || decoded.Libraries[0].Name != "movies" {
		t.Errorf("decoded = %+v", decoded)
	}
}

// TestStatusCommand_ExitsZeroEvenWhenFilesHaveFailed proves the CLI's exit
// code reflects whether the request itself succeeded, not the sync outcome
// being reported — a clean status call showing failed files still exits 0.
func TestStatusCommand_ExitsZeroEvenWhenFilesHaveFailed(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		total, limit, offset := 5, 100, 0
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.StatusResponse{
			Libraries: []api.LibrarySummaryEntry{{Name: "movies", Failed: 5}},
			Files:     make([]api.FileEntry, 5),
			Total:     &total, Limit: &limit, Offset: &offset,
		})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"status", "--library", "movies", "--api", apiURL})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error even though the API call itself succeeded: %v", err)
	}
}

func TestStatusCommand_DaemonErrorSurfacesMessageAndNonZeroExit(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: "not_found", Message: "unknown library \"nope\""})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"status", "--library", "nope", "--api", apiURL})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown library")
	}
	if !strings.Contains(err.Error(), "unknown library") {
		t.Errorf("error = %v, want it to surface the daemon's message", err)
	}
}

package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/api"
	"github.com/aaronkyriesenbach/sublime/internal/cli"
)

func newFakeAPIServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestLibrariesCommand_PrintsRegisteredLibrariesHumanReadable(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/libraries" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		resp := api.LibrariesResponse{Libraries: []api.LibraryEntry{
			{Name: "movies", Path: "/media/movies", Languages: []string{"en", "pt-BR"}, Pending: 1, Synced: 2, Failed: 3},
			{Name: "tv-anime", Path: "/media/tv", Languages: []string{"en"}, Pending: 0, Synced: 0, Failed: 0},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--api", apiURL})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := out.String()
	for _, want := range []string{"movies", "/media/movies", "en, pt-BR", "tv-anime", "/media/tv", "1", "2", "3"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q; got:\n%s", want, output)
		}
	}
}

func TestLibrariesCommand_JSONFlagPrintsRawAPIResponse(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := api.LibrariesResponse{Libraries: []api.LibraryEntry{
			{Name: "movies", Path: "/media/movies", Languages: []string{"en"}},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--api", apiURL, "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var decoded api.LibrariesResponse
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\noutput:\n%s", err, out.String())
	}
	if len(decoded.Libraries) != 1 || decoded.Libraries[0].Name != "movies" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestLibrariesCommand_DaemonUnreachableExitsNonZero(t *testing.T) {
	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--api", "http://127.0.0.1:1"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error when the daemon is unreachable")
	}
}

func TestLibrariesCommand_DaemonErrorEnvelopeSurfacesMessage(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: "internal_error", Message: "boom"})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--api", apiURL})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error when the daemon returns a non-2xx response")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to surface the daemon's message", err)
	}
}

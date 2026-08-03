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

func TestReprocessCommand_PostsPathAndPrintsAcceptedMessage(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/reprocess" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}

		var req api.ReprocessRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if req.Path != "/media/movies/a.mkv" {
			t.Errorf("path = %q, want /media/movies/a.mkv", req.Path)
		}

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(api.ReprocessResponse{
			Accepted: true,
			Scope:    api.ReprocessScope(req),
		})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"reprocess", "/media/movies/a.mkv", "--api", apiURL})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "/media/movies/a.mkv") {
		t.Errorf("output missing scoped path; got:\n%s", output)
	}
}

func TestReprocessCommand_RequiresAPathArgument(t *testing.T) {
	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"reprocess"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error when no path is given")
	}
}

func TestReprocessCommand_JSONFlagPrintsRawAPIResponse(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(api.ReprocessResponse{
			Accepted: true,
			Scope:    api.ReprocessScope{Path: "/media/movies/a.mkv"},
		})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"reprocess", "/media/movies/a.mkv", "--api", apiURL, "--json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var decoded api.ReprocessResponse
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\noutput:\n%s", err, out.String())
	}
	if !decoded.Accepted || decoded.Scope.Path != "/media/movies/a.mkv" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestReprocessCommand_UnknownPathSurfacesErrorAndNonZeroExit(t *testing.T) {
	apiURL := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{
			Error:   "not_found",
			Message: "path is not part of any configured library",
		})
	})

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"reprocess", "/not/a/library", "--api", apiURL})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown path")
	}
	if !strings.Contains(err.Error(), "not part of any configured library") {
		t.Errorf("error = %v, want it to surface the daemon's message", err)
	}
}

func TestReprocessCommand_DaemonUnreachableExitsNonZero(t *testing.T) {
	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"reprocess", "/media/movies/a.mkv", "--api", "http://127.0.0.1:1"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error when the daemon is unreachable")
	}
}

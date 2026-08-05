package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/api"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

type statusResponse = api.StatusResponse

type errorEnvelope = api.ErrorEnvelope

func testLibraries() []domain.Library {
	return []domain.Library{
		{Name: "movies", Path: "/media/movies", Languages: []language.Tag{language.English}},
		{Name: "tv", Path: "/media/tv", Languages: []language.Tag{language.English}},
	}
}

func TestStatus_UnscopedReturnsAllLibrarySummariesOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	en := mustLang(t, "en")

	f, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, f.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := st.MarkFailed(ctx, f.ID, en, domain.FailureNoCandidate); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(body.Libraries) != 2 {
		t.Fatalf("libraries = %+v, want 2 entries (all configured libraries)", body.Libraries)
	}
	if body.Files != nil {
		t.Errorf("files = %+v, want nil (unscoped request)", body.Files)
	}
	if body.Total != nil || body.Limit != nil || body.Offset != nil {
		t.Errorf("pagination fields should be absent when unscoped: total=%v limit=%v offset=%v", body.Total, body.Limit, body.Offset)
	}
}

func TestStatus_LibrarySummaryIncludesInProgressCount(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	en := mustLang(t, "en")

	f, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, f.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := st.MarkInProgress(ctx, f.ID, en); err != nil {
		t.Fatalf("MarkInProgress: %v", err)
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if !strings.Contains(string(raw), `"in_progress":1`) {
		t.Errorf("expected response to contain \"in_progress\":1, got %s", raw)
	}

	var body statusResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	for _, lib := range body.Libraries {
		if lib.Name == "movies" && lib.InProgress != 1 {
			t.Errorf("movies in_progress = %d, want 1", lib.InProgress)
		}
	}
}

func TestStatus_ScopedByLibraryDefaultsToPendingAndFailed(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	en := mustLang(t, "en")

	synced, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/synced.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, synced.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := st.MarkSynced(ctx, synced.ID, en); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	failed, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/failed.mkv", "hash-2")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, failed.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := st.MarkFailed(ctx, failed.ID, en, domain.FailureSyncFailed); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	inProgress, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/inprogress.mkv", "hash-3")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, inProgress.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := st.MarkInProgress(ctx, inProgress.ID, en); err != nil {
		t.Fatalf("MarkInProgress: %v", err)
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=movies")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(body.Libraries) != 1 || body.Libraries[0].Name != "movies" {
		t.Fatalf("libraries = %+v, want a single movies summary", body.Libraries)
	}
	if body.Libraries[0].InProgress != 1 {
		t.Errorf("in_progress = %d, want 1", body.Libraries[0].InProgress)
	}
	if len(body.Files) != 2 {
		t.Fatalf("files = %+v, want the failed and in-progress files (synced omitted by default)", body.Files)
	}
	var gotPaths []string
	for _, f := range body.Files {
		gotPaths = append(gotPaths, f.Path)
	}
	for _, want := range []string{failed.Path, inProgress.Path} {
		found := false
		for _, p := range gotPaths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in results, got %v", want, gotPaths)
		}
	}
	for _, f := range body.Files {
		if f.Path == failed.Path {
			if f.Languages["en"].Status != "failed" || f.Languages["en"].Reason != "sync_failed" {
				t.Errorf("en language state = %+v", f.Languages["en"])
			}
		}
		if f.Path == inProgress.Path {
			if f.Languages["en"].Status != "in_progress" {
				t.Errorf("en language state = %+v, want status in_progress", f.Languages["en"])
			}
		}
	}
	if body.Total == nil || *body.Total != 2 {
		t.Errorf("total = %v, want 2", body.Total)
	}
}

func TestStatus_StateAllIncludesSyncedFiles(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	en := mustLang(t, "en")

	synced, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/synced.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, synced.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := st.MarkSynced(ctx, synced.ID, en); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=movies&state=all")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Files) != 1 || body.Files[0].Path != synced.Path {
		t.Fatalf("files = %+v, want the synced file included with ?state=all", body.Files)
	}
}

func TestStatus_ScopedByPathAppliesADirectoryPrefix(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	en := mustLang(t, "en")

	inScope, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/season1/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, inScope.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	outOfScope, err := st.ObserveFileContentHash(ctx, "movies", "/media/movies/season2/b.mkv", "hash-2")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := st.EnsureLanguage(ctx, outOfScope.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?path=" + "/media/movies/season1")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Files) != 1 || body.Files[0].Path != inScope.Path {
		t.Fatalf("files = %+v, want only season1/a.mkv", body.Files)
	}
	if len(body.Libraries) != 1 || body.Libraries[0].Name != "movies" {
		t.Errorf("libraries = %+v, want the resolved owning library only", body.Libraries)
	}
}

func TestStatus_PaginatesWithLimitAndOffset(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	en := mustLang(t, "en")

	for i := range 3 {
		f, err := st.ObserveFileContentHash(ctx, "movies", fileNameForIndex(i), "hash")
		if err != nil {
			t.Fatalf("ObserveFileContentHash: %v", err)
		}
		if err := st.EnsureLanguage(ctx, f.ID, en); err != nil {
			t.Fatalf("EnsureLanguage: %v", err)
		}
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=movies&limit=2&offset=0")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(body.Files))
	}
	if body.Total == nil || *body.Total != 3 {
		t.Errorf("total = %v, want 3", body.Total)
	}
	if body.Limit == nil || *body.Limit != 2 {
		t.Errorf("limit = %v, want 2", body.Limit)
	}
	if body.Offset == nil || *body.Offset != 0 {
		t.Errorf("offset = %v, want 0", body.Offset)
	}
}

func fileNameForIndex(i int) string {
	return "/media/movies/file" + string(rune('a'+i)) + ".mkv"
}

func TestStatus_UnknownLibraryReturns404WithErrorEnvelope(t *testing.T) {
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=nope")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	var body errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Error == "" || body.Message == "" {
		t.Errorf("error envelope = %+v, want both fields set", body)
	}
}

func TestStatus_PathOutsideAnyLibraryReturns404(t *testing.T) {
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?path=/not/a/library/path")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestStatus_MismatchedLibraryAndPathReturns400(t *testing.T) {
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=tv&path=/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestStatus_InvalidLimitReturns400(t *testing.T) {
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=movies&limit=notanumber")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestStatus_InvalidStateReturns400(t *testing.T) {
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status?library=movies&state=bogus")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestStatus_RejectsNonGET(t *testing.T) {
	srv := api.NewServer(api.Deps{Store: openTestStore(t), Libraries: testLibraries()})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/status", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

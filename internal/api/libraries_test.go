package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/api"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "sublime.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustLang(t *testing.T, tag string) language.Tag {
	t.Helper()
	parsed, err := language.Parse(tag)
	if err != nil {
		t.Fatalf("parsing language tag %q: %v", tag, err)
	}
	return parsed
}

type librariesResponse = api.LibrariesResponse

func TestLibraries_ListsConfiguredLibrariesWithZeroCountsWhenUntouched(t *testing.T) {
	st := openTestStore(t)
	libs := []domain.Library{
		{Name: "movies", Path: "/media/movies", Languages: []language.Tag{language.English, language.BrazilianPortuguese}},
		{Name: "tv", Path: "/media/tv", Languages: []language.Tag{language.English}},
	}

	srv := api.NewServer(api.Deps{Store: st, Libraries: libs})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/libraries")
	if err != nil {
		t.Fatalf("GET /libraries: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body librariesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(body.Libraries) != 2 {
		t.Fatalf("libraries = %d, want 2: %+v", len(body.Libraries), body.Libraries)
	}

	movies := body.Libraries[0]
	if movies.Name != "movies" || movies.Path != "/media/movies" {
		t.Errorf("movies entry = %+v", movies)
	}
	if len(movies.Languages) != 2 || movies.Languages[0] != "en" || movies.Languages[1] != "pt-BR" {
		t.Errorf("movies languages = %+v, want [en pt-BR]", movies.Languages)
	}
	if movies.Pending != 0 || movies.Synced != 0 || movies.Failed != 0 {
		t.Errorf("movies counts = %+v, want all zero for an untouched library", movies)
	}
}

func TestLibraries_ReflectsStoreCounts(t *testing.T) {
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
	if err := st.MarkSynced(ctx, f.ID, en); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	libs := []domain.Library{{Name: "movies", Path: "/media/movies", Languages: []language.Tag{en}}}
	srv := api.NewServer(api.Deps{Store: st, Libraries: libs})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/libraries")
	if err != nil {
		t.Fatalf("GET /libraries: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body librariesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Libraries) != 1 || body.Libraries[0].Synced != 1 {
		t.Fatalf("libraries = %+v, want a single movies entry with Synced=1", body.Libraries)
	}
}

func TestLibraries_RejectsNonGET(t *testing.T) {
	srv := api.NewServer(api.Deps{})
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/libraries", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /libraries: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

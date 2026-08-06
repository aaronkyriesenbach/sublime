package subdl_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/subdl"
)

// fakeClock records requested delays instead of sleeping, mirroring
// opensubtitles' own test convention.
type fakeClock struct {
	delays []time.Duration
}

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.delays = append(f.delays, d)
	return nil
}

func newTestProvider(t *testing.T, mock *mockServer, clock *fakeClock, paid bool) *subdl.Provider {
	t.Helper()
	p, err := subdl.New(subdl.Config{
		APIKey:  "test-api-key",
		Paid:    paid,
		BaseURL: mock.URL(),
		Clock:   clock,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return p
}

func searchItem(release, name string, year, season, episode, nID, fileNID int) map[string]any {
	return map[string]any{
		"release_name":   release,
		"name":           name,
		"year":           year,
		"season_number":  season,
		"episode_number": episode,
		"n_id":           nID,
		"file_n_id":      fileNID,
	}
}

func searchResponse(items ...map[string]any) map[string]any {
	return map[string]any{"status": true, "subtitles": items}
}

// --- New() validation ---

func TestNew_RequiresAPIKey(t *testing.T) {
	if _, err := subdl.New(subdl.Config{}); err == nil {
		t.Fatal("expected an error when APIKey is missing")
	}
}

// --- Search ---

func TestSearch_MovieQuery_SendsFilmNameAndYear(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English}
	if _, err := p.Search(context.Background(), query); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 1 {
		t.Fatalf("got %d /subtitles requests, want 1", len(reqs))
	}
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}

	wantParams := map[string]string{
		"film_name": "Arrival",
		"year":      "2016",
		"languages": "en",
		"api_key":   "test-api-key",
	}
	for k, want := range wantParams {
		if got := params.Get(k); got != want {
			t.Errorf("param %q = %q, want %q", k, got, want)
		}
	}
	for _, forbidden := range []string{"full_season", "unpack", "season_number", "episode_number"} {
		if params.Has(forbidden) {
			t.Errorf("movie query must not send %q, got query %q", forbidden, reqs[0].Query)
		}
	}
}

func TestSearch_TVQuery_SendsSeasonAndEpisode(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	query := provider.Query{Title: "Community", Season: 2, Episode: 1, Language: language.English}
	if _, err := p.Search(context.Background(), query); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 1 {
		t.Fatalf("got %d /subtitles requests, want 1", len(reqs))
	}
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}

	wantParams := map[string]string{
		"film_name":      "Community",
		"season_number":  "2",
		"episode_number": "1",
	}
	for k, want := range wantParams {
		if got := params.Get(k); got != want {
			t.Errorf("param %q = %q, want %q", k, got, want)
		}
	}
	for _, forbidden := range []string{"full_season", "unpack"} {
		if params.Has(forbidden) {
			t.Errorf("TV query must never send %q, got query %q", forbidden, reqs[0].Query)
		}
	}
}

func TestSearch_LanguageTranslation_PtBROverride(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	ptBR, err := language.Parse("pt-BR")
	if err != nil {
		t.Fatalf("language.Parse(pt-BR) error = %v", err)
	}

	query := provider.Query{Title: "Arrival", Year: 2016, Language: ptBR}
	if _, err := p.Search(context.Background(), query); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}
	if got := params.Get("languages"); got != "pob" {
		t.Errorf("languages param = %q, want %q (pt-BR override)", got, "pob")
	}
}

func TestSearch_LanguageTranslation_FallsBackToLowercaseISO6391(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	frCA, err := language.Parse("fr-CA")
	if err != nil {
		t.Fatalf("language.Parse(fr-CA) error = %v", err)
	}

	query := provider.Query{Title: "Arrival", Year: 2016, Language: frCA}
	if _, err := p.Search(context.Background(), query); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}
	if got := params.Get("languages"); got != "fr" {
		t.Errorf("languages param = %q, want %q (no override, base ISO 639-1)", got, "fr")
	}
}

func TestSearch_ConvertsResponseItemsToCandidates(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 1234, 1),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}

	want := domain.Candidate{
		ID:           "1234-1",
		Title:        "Arrival",
		Year:         2016,
		Source:       "BluRay",
		ReleaseGroup: "GROUP",
		Resolution:   "1080p",
		Codec:        "x264",
		HashMatch:    false,
	}
	if candidates[0] != want {
		t.Errorf("candidate = %+v, want %+v", candidates[0], want)
	}
}

func TestSearch_MultipleItems_YieldsOneCandidateEach(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 1234, 1),
		searchItem("Arrival.2016.720p.WEBRip.x265-OTHER", "Arrival", 2016, 0, 0, 5678, 1),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	query := provider.Query{Title: "Arrival", Year: 2016}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(candidates))
	}
	for _, c := range candidates {
		if c.HashMatch {
			t.Errorf("candidate %+v has HashMatch = true, want false (SubDL has no hash search)", c)
		}
	}
}

func TestSearch_NoResults_ReturnsEmptyNotError(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	candidates, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("got %d candidates, want 0", len(candidates))
	}
}

// --- Download ---

func TestDownload_UnpaidOmitsAPIKey(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/download", rawHandler(http.StatusOK, "subtitle content"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	data, err := p.Download(context.Background(), domain.Candidate{ID: "1234-1"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(data) != "subtitle content" {
		t.Errorf("Download() = %q, want %q", data, "subtitle content")
	}

	reqs := mock.requestsFor(http.MethodGet, "/download")
	if len(reqs) != 1 {
		t.Fatalf("got %d /download requests, want 1", len(reqs))
	}
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}
	if params.Has("api_key") {
		t.Errorf("unpaid download query %q must not include api_key", reqs[0].Query)
	}
	if got, want := params.Get("n_id"), "1234"; got != want {
		t.Errorf("n_id = %q, want %q", got, want)
	}
	if got, want := params.Get("file_n_id"), "1"; got != want {
		t.Errorf("file_n_id = %q, want %q", got, want)
	}
}

func TestDownload_PaidAttachesAPIKey(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/download", rawHandler(http.StatusOK, "subtitle content"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, true)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "1234-1"}); err != nil {
		t.Fatalf("Download() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/download")
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}
	if got, want := params.Get("api_key"), "test-api-key"; got != want {
		t.Errorf("api_key = %q, want %q", got, want)
	}
}

func TestDownload_InvalidCandidateID(t *testing.T) {
	mock := newMockServer(t)
	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "not-valid"}); err == nil {
		t.Fatal("expected an error for a malformed candidate ID")
	}
}

// --- Retry/backoff integration (shared retry.Pacer + retry.Executor) ---

func TestSearch_RetriesOn429ThenSucceeds(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusTooManyRequests, map[string]any{"status": false, "error": "slow down"}))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 1234, 1),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	candidates, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	if len(clock.delays) == 0 {
		t.Fatal("expected at least one recorded delay from the retry executor")
	}
}

func TestSearch_NonRetryableClientError_FailsImmediately(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusBadRequest, map[string]any{"status": false, "error": "bad query"}))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016}); err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if len(clock.delays) != 0 {
		t.Errorf("recorded delays = %v, want none (400 is terminal, not retried)", clock.delays)
	}
}

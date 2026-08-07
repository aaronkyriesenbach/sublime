package subdl_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
		APIKey:          "test-api-key",
		Paid:            paid,
		BaseURL:         mock.URL(),
		DownloadBaseURL: mock.URL(),
		Clock:           clock,
		Now:             func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return p
}

// searchItem builds one entry of a /subtitles response's "subtitles"
// array: release (release_name), a download url (relative to
// dl.subdl.com, e.g. "/subtitle/1234-1.zip"), and season/episode (0 for a
// movie or full-season pack). There is no per-item title or year field --
// see searchResponseWithTitle.
func searchItem(release, url string, season, episode int) map[string]any {
	return map[string]any{
		"release_name": release,
		"url":          url,
		"season":       season,
		"episode":      episode,
	}
}

// searchResponse builds a /subtitles response with no "results" entry, for
// tests that only care about the outgoing request, not the returned
// Candidates.
func searchResponse(items ...map[string]any) map[string]any {
	return map[string]any{"status": true, "subtitles": items}
}

// searchResponseWithTitle builds a /subtitles response with a single
// "results" entry (title, year), matching SubDL's documented shape: every
// item in "subtitles" belongs to that one results[0] identity.
func searchResponseWithTitle(title string, year int, items ...map[string]any) map[string]any {
	return map[string]any{
		"status":    true,
		"results":   []map[string]any{{"name": title, "year": year}},
		"subtitles": items,
	}
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
		"film_name":     "Arrival",
		"year":          "2016",
		"languages":     "en",
		"api_key":       "test-api-key",
		"subs_per_page": "30",
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
		"subs_per_page":  "30",
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
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponseWithTitle("Arrival", 2016,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1234-1.zip", 0, 0),
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
		ID:           "/subtitle/1234-1.zip",
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
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponseWithTitle("Arrival", 2016,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1234-1.zip", 0, 0),
		searchItem("Arrival.2016.720p.WEBRip.x265-OTHER", "/subtitle/5678-1.zip", 0, 0),
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

// --- Paging (ADR 0010, issue #78) ---

// pagedSearchResponse builds a /subtitles response like
// searchResponseWithTitle, plus the totalPages/currentPage fields SubDL's
// paging protocol reports.
func pagedSearchResponse(title string, year int, currentPage, totalPages int, items ...map[string]any) map[string]any {
	body := searchResponseWithTitle(title, year, items...)
	body["currentPage"] = currentPage
	body["totalPages"] = totalPages
	return body
}

func TestSearch_SinglePage_WhenCurrentPageEqualsTotalPages_IssuesOneRequest(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, 1, 1,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1234-1.zip", 0, 0),
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

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 1 {
		t.Fatalf("got %d /subtitles requests, want 1 (currentPage already == totalPages)", len(reqs))
	}
}

func TestSearch_MultiplePages_WalksAndMergesCandidates(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, 1, 3,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1-1.zip", 0, 0),
	)))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, 2, 3,
		searchItem("Arrival.2016.720p.WEBRip.x265-OTHER", "/subtitle/2-1.zip", 0, 0),
	)))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, 3, 3,
		searchItem("Arrival.2016.2160p.WEBRip.x265-THIRD", "/subtitle/3-1.zip", 0, 0),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	candidates, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3 (merged across all 3 pages)", len(candidates))
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 3 {
		t.Fatalf("got %d /subtitles requests, want 3", len(reqs))
	}
	for i, req := range reqs {
		params, err := url.ParseQuery(req.Query)
		if err != nil {
			t.Fatalf("parsing query %q: %v", req.Query, err)
		}
		wantPage := strconv.Itoa(i + 1)
		if got := params.Get("page"); got != wantPage {
			t.Errorf("request %d: page param = %q, want %q", i, got, wantPage)
		}
		if got := params.Get("subs_per_page"); got != "30" {
			t.Errorf("request %d: subs_per_page param = %q, want %q", i, got, "30")
		}
	}
}

func TestSearch_StopsAtPageCapEvenIfMorePagesReported(t *testing.T) {
	mock := newMockServer(t)
	for page := 1; page <= 3; page++ {
		mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, page, 10,
			searchItem(fmt.Sprintf("Arrival.2016.page%d.BluRay.x264-GROUP", page), fmt.Sprintf("/subtitle/%d-1.zip", page), 0, 0),
		)))
	}

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	candidates, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3 (capped even though totalPages=10)", len(candidates))
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 3 {
		t.Fatalf("got %d /subtitles requests, want 3 (fixed cap, not adaptive to totalPages)", len(reqs))
	}
}

func TestSearch_LaterPageFailure_DiscardsEarlierPagesAndReturnsError(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, 1, 3,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1-1.zip", 0, 0),
	)))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusBadRequest, map[string]any{"status": false, "error": "bad query"}))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	candidates, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})
	if err == nil {
		t.Fatal("expected an error when a later page request fails")
	}
	if candidates != nil {
		t.Errorf("got candidates %+v, want nil (earlier-page candidates must be discarded)", candidates)
	}
}

func TestSearch_LaterPageQuotaExhausted_DiscardsEarlierPagesAndSuspends(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, pagedSearchResponse("Arrival", 2016, 1, 3,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1-1.zip", 0, 0),
	)))
	mock.on(http.MethodGet, "/subtitles", quotaExceededHandler())

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	candidates, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})
	var quotaErr *provider.QuotaExhaustedError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("Search() error = %v, want a *provider.QuotaExhaustedError", err)
	}
	if candidates != nil {
		t.Errorf("got candidates %+v, want nil (earlier-page candidates must be discarded)", candidates)
	}

	if _, suspended := p.Suspension(); !suspended {
		t.Error("expected Provider to be Suspended after a quota-exhaustion response mid-walk")
	}
}

// --- Download ---

func TestDownload_UnpaidOmitsAPIKey(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitle/1234-1.zip", rawHandler(http.StatusOK, "subtitle content"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	data, err := p.Download(context.Background(), domain.Candidate{ID: "/subtitle/1234-1.zip"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(data) != "subtitle content" {
		t.Errorf("Download() = %q, want %q", data, "subtitle content")
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitle/1234-1.zip")
	if len(reqs) != 1 {
		t.Fatalf("got %d download requests, want 1", len(reqs))
	}
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}
	if params.Has("api_key") {
		t.Errorf("unpaid download query %q must not include api_key", reqs[0].Query)
	}
}

// TestDownload_UnpaidStripsEmbeddedAPIKeyFromCandidateURL guards against
// SubDL's search response sometimes echoing an api_key already embedded in
// a subtitle's "url" field (observed live against the real API): Paid
// alone must decide whether a key is sent, never whatever Search happened
// to return.
func TestDownload_UnpaidStripsEmbeddedAPIKeyFromCandidateURL(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitle/1234-1.zip", rawHandler(http.StatusOK, "subtitle content"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "/subtitle/1234-1.zip?api_key=leaked-key"}); err != nil {
		t.Fatalf("Download() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitle/1234-1.zip")
	params, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("parsing query %q: %v", reqs[0].Query, err)
	}
	if params.Has("api_key") {
		t.Errorf("unpaid download query %q must not include api_key, even if the candidate URL already had one", reqs[0].Query)
	}
}

func TestDownload_PaidAttachesAPIKey(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitle/1234-1.zip", rawHandler(http.StatusOK, "subtitle content"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, true)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "/subtitle/1234-1.zip"}); err != nil {
		t.Fatalf("Download() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitle/1234-1.zip")
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
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponseWithTitle("Arrival", 2016,
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "/subtitle/1234-1.zip", 0, 0),
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

// --- Quota exhaustion / Suspension (ADR 0006/0007, issue #70) ---

func quotaExceededHandler() func(w http.ResponseWriter, r *http.Request) {
	return jsonHandler(http.StatusTooManyRequests, map[string]any{"status": false, "error": "quota_exceeded"})
}

// TestSearch_QuotaExhausted_429WithResetHeader covers ADR 0007's one
// recognized shape from a search request: a 429 with
// {"error":"quota_exceeded"}, with ResumeAt parsed from X-RateLimit-Reset.
func TestSearch_QuotaExhausted_429WithResetHeader(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", withHeader("X-RateLimit-Reset", "1767225600", quotaExceededHandler()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	_, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})

	var quotaErr *provider.QuotaExhaustedError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("Search() error = %v, want a *provider.QuotaExhaustedError", err)
	}
	if want := time.Unix(1767225600, 0).UTC(); !quotaErr.ResumeAt.Equal(want) {
		t.Errorf("ResumeAt = %v, want %v", quotaErr.ResumeAt, want)
	}

	// Quota exhaustion is terminal: no retry attempts.
	if reqs := mock.requestsFor(http.MethodGet, "/subtitles"); len(reqs) != 1 {
		t.Errorf("got %d /subtitles requests, want 1 (no retry on quota exhaustion)", len(reqs))
	}
	if len(clock.delays) != 0 {
		t.Errorf("recorded delays = %v, want none", clock.delays)
	}
}

// TestSearch_QuotaExhausted_NoResetHeader_FallsBackToNextUTCMidnight covers
// ADR 0007's fallback: a calendar-day reset, not a flat duration.
func TestSearch_QuotaExhausted_NoResetHeader_FallsBackToNextUTCMidnight(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", quotaExceededHandler())

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false) // newTestProvider's fixed Now is 2026-01-01T00:00:00Z

	_, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016})

	var quotaErr *provider.QuotaExhaustedError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("Search() error = %v, want a *provider.QuotaExhaustedError", err)
	}
	if want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC); !quotaErr.ResumeAt.Equal(want) {
		t.Errorf("ResumeAt = %v, want %v (next UTC midnight)", quotaErr.ResumeAt, want)
	}
}

// TestDownload_Paid_QuotaExhausted_429WithResetHeader covers ADR 0007's
// documented shape from an authenticated download request.
func TestDownload_Paid_QuotaExhausted_429WithResetHeader(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitle/1234-1.zip", withHeader("X-RateLimit-Reset", "1767225600", quotaExceededHandler()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, true)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "/subtitle/1234-1.zip"})

	var quotaErr *provider.QuotaExhaustedError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("Download() error = %v, want a *provider.QuotaExhaustedError", err)
	}
	if want := time.Unix(1767225600, 0).UTC(); !quotaErr.ResumeAt.Equal(want) {
		t.Errorf("ResumeAt = %v, want %v", quotaErr.ResumeAt, want)
	}
}

// TestDownload_Anonymous_NonQuotaShapeNeverSuspends is ADR 0007's explicit
// scope boundary: the anonymous download path's rejection response has no
// documented shape and must never be pattern-matched into a
// provider.QuotaExhaustedError, even one shaped exactly like the
// documented quota_exceeded response -- it's just an ordinary Download
// error for that one (file, language) pair.
func TestDownload_Anonymous_NonQuotaShapeNeverSuspends(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitle/1234-1.zip", quotaExceededHandler())

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "/subtitle/1234-1.zip"})
	if err == nil {
		t.Fatal("expected an error for a 429 anonymous download response")
	}

	var quotaErr *provider.QuotaExhaustedError
	if errors.As(err, &quotaErr) {
		t.Fatalf("Download() error = %v, want an ordinary error, not a *provider.QuotaExhaustedError", err)
	}
	if resumeAt, suspended := p.Suspension(); suspended {
		t.Errorf("Suspension() after anonymous rejection = (%v, %v), want suspended=false", resumeAt, suspended)
	}
}

// TestQuotaSuspension_ShortCircuitsUntilResumeTimeThenResumes drives the
// Provider's full Suspended lifecycle: a quota-exhaustion response
// suspends it; a subsequent Search/Download before the resume time makes
// no HTTP request at all and returns the same signal; once the resume
// time passes, a further call issues a real request again.
func TestQuotaSuspension_ShortCircuitsUntilResumeTimeThenResumes(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", quotaExceededHandler())

	clock := &fakeClock{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := subdl.New(subdl.Config{
		APIKey:  "test-api-key",
		BaseURL: mock.URL(),
		Clock:   clock,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = p.Search(context.Background(), provider.Query{Title: "Arrival"})
	var quotaErr *provider.QuotaExhaustedError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("first Search() error = %v, want a *provider.QuotaExhaustedError", err)
	}
	resumeAt := quotaErr.ResumeAt

	// Still before resumeAt: no handler queued for /subtitles, so a real
	// request would fail the mock server outright.
	now = now.Add(time.Hour)
	_, err = p.Search(context.Background(), provider.Query{Title: "Arrival"})
	var secondQuotaErr *provider.QuotaExhaustedError
	if !errors.As(err, &secondQuotaErr) {
		t.Fatalf("second Search() error = %v, want a *provider.QuotaExhaustedError", err)
	}
	if !secondQuotaErr.ResumeAt.Equal(resumeAt) {
		t.Errorf("second ResumeAt = %v, want %v (unchanged while still suspended)", secondQuotaErr.ResumeAt, resumeAt)
	}
	if reqs := mock.requestsFor(http.MethodGet, "/subtitles"); len(reqs) != 1 {
		t.Errorf("got %d /subtitles requests, want 1 (suspended call must not hit the network)", len(reqs))
	}

	// Past resumeAt: a real request should go out again.
	now = resumeAt.Add(time.Minute)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival"}); err != nil {
		t.Fatalf("third Search() error = %v, want success after resume time", err)
	}
	if reqs := mock.requestsFor(http.MethodGet, "/subtitles"); len(reqs) != 2 {
		t.Errorf("got %d /subtitles requests, want 2 (a real request after resuming)", len(reqs))
	}
}

// TestSuspension_ReportsSuspendedAndResumeTime covers the Provider's
// suspensionReporter capability (internal/pipeline/production.go): a
// not-yet-suspended Provider reports suspended=false, a live suspension
// reports suspended=true with the resume time, and a resume time that has
// passed reports suspended=false again.
func TestSuspension_ReportsSuspendedAndResumeTime(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", quotaExceededHandler())

	clock := &fakeClock{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := subdl.New(subdl.Config{
		APIKey:  "test-api-key",
		BaseURL: mock.URL(),
		Clock:   clock,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if resumeAt, suspended := p.Suspension(); suspended {
		t.Errorf("Suspension() before any exhaustion = (%v, %v), want suspended=false", resumeAt, suspended)
	}

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival"}); err == nil {
		t.Fatal("expected Search() to fail with quota exhaustion")
	}

	wantResumeAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	resumeAt, suspended := p.Suspension()
	if !suspended {
		t.Fatal("Suspension() after exhaustion = suspended=false, want true")
	}
	if !resumeAt.Equal(wantResumeAt) {
		t.Errorf("Suspension() resumeAt = %v, want %v", resumeAt, wantResumeAt)
	}

	now = wantResumeAt.Add(time.Minute)
	if _, suspended := p.Suspension(); suspended {
		t.Error("Suspension() past resume time = suspended=true, want false")
	}
}

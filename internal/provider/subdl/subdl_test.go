package subdl_test

import (
	"context"
	"errors"
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
		Now:     func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
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
	mock.on(http.MethodGet, "/download", withHeader("X-RateLimit-Reset", "1767225600", quotaExceededHandler()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, true)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "1234-1"})

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
	mock.on(http.MethodGet, "/download", quotaExceededHandler())

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock, false)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "1234-1"})
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

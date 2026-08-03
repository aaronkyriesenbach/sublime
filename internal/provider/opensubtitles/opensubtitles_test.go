package opensubtitles_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/opensubtitles"
)

// fakeClock records requested delays instead of sleeping, so retry/backoff
// tests run instantly and deterministically — mirrors internal/retry's own
// test convention.
type fakeClock struct {
	delays []time.Duration
}

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.delays = append(f.delays, d)
	return nil
}

func testSecrets() config.OpenSubtitlesSecrets {
	return config.OpenSubtitlesSecrets{APIKey: "test-api-key", Username: "user", Password: "pass"}
}

func newTestProvider(t *testing.T, mock *mockServer, clock *fakeClock) *opensubtitles.Provider {
	t.Helper()
	p, err := opensubtitles.New(opensubtitles.Config{
		Secrets: testSecrets(),
		BaseURL: mock.URL(),
		Clock:   clock,
		Now:     func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return p
}

func searchItem(release, title string, year, season, episode int, fileIDs ...int) map[string]any {
	files := make([]map[string]any, len(fileIDs))
	for i, id := range fileIDs {
		files[i] = map[string]any{"file_id": id, "cd_number": 1, "file_name": fmt.Sprintf("file%d.srt", id)}
	}
	return map[string]any{
		"attributes": map[string]any{
			"release": release,
			"feature_details": map[string]any{
				"title":          title,
				"year":           year,
				"season_number":  season,
				"episode_number": episode,
			},
			"files": files,
		},
	}
}

func searchResponse(items ...map[string]any) map[string]any {
	return map[string]any{"data": items}
}

func writeVideoFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mkv")
	if err := os.WriteFile(path, []byte("not a real video, just needs bytes to hash"), 0o644); err != nil {
		t.Fatalf("writing test video file: %v", err)
	}
	return path
}

// --- New() validation ---

func TestNew_RequiresAPIKey(t *testing.T) {
	_, err := opensubtitles.New(opensubtitles.Config{
		Secrets: config.OpenSubtitlesSecrets{Username: "u", Password: "p"},
	})
	if err == nil {
		t.Fatal("expected an error when APIKey is missing")
	}
}

func TestNew_RequiresUsernameAndPassword(t *testing.T) {
	_, err := opensubtitles.New(opensubtitles.Config{
		Secrets: config.OpenSubtitlesSecrets{APIKey: "key"},
	})
	if err == nil {
		t.Fatal("expected an error when Username/Password are missing")
	}
}

// --- Search ---

func TestSearch_HashMatchFound_SkipsFuzzyFallback(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 111),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English, Path: writeVideoFile(t)}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	want := domain.Candidate{
		ID: "111", Title: "Arrival", Year: 2016,
		Source: "BluRay", ReleaseGroup: "GROUP", Codec: "x264", Resolution: "1080p",
		HashMatch: true,
	}
	if candidates[0] != want {
		t.Errorf("candidate = %+v, want %+v", candidates[0], want)
	}

	if reqs := mock.requestsFor(http.MethodGet, "/subtitles"); len(reqs) != 1 {
		t.Errorf("got %d /subtitles requests, want exactly 1 (fuzzy fallback should be skipped)", len(reqs))
	}
}

func TestSearch_HashMatchRequestParams(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse())) // hash search: empty
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse())) // fuzzy fallback: empty

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	videoPath := writeVideoFile(t)
	wantHash, err := opensubtitles.ComputeMovieHash(videoPath)
	if err != nil {
		t.Fatalf("ComputeMovieHash() error = %v", err)
	}

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English, Path: videoPath}
	if _, err := p.Search(context.Background(), query); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 2 { // hash search (empty) + fuzzy fallback
		t.Fatalf("got %d /subtitles requests, want 2", len(reqs))
	}

	first := reqs[0]
	got := first.Query
	if want := fmt.Sprintf("languages=en&moviehash=%s&moviehash_match=only", wantHash); got != want {
		t.Errorf("hash search query = %q, want %q", got, want)
	}
	if got := first.Headers.Get("Api-Key"); got != "test-api-key" {
		t.Errorf("Api-Key header = %q, want %q", got, "test-api-key")
	}
	if got := first.Headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization header = %q, want none on a search request", got)
	}
}

func TestSearch_NoHashMatch_FallsBackToFuzzySearch(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse())) // hash search: empty
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.720p.WEBRip.x265-OTHER", "Arrival", 2016, 0, 0, 222),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English, Path: writeVideoFile(t)}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	if candidates[0].HashMatch {
		t.Error("fuzzy-fallback candidate has HashMatch = true, want false")
	}
	if candidates[0].ID != "222" {
		t.Errorf("candidate ID = %q, want %q", candidates[0].ID, "222")
	}

	reqs := mock.requestsFor(http.MethodGet, "/subtitles")
	if len(reqs) != 2 {
		t.Fatalf("got %d /subtitles requests, want 2 (hash then fuzzy)", len(reqs))
	}

	fuzzyParams, err := url.ParseQuery(reqs[1].Query)
	if err != nil {
		t.Fatalf("parsing fuzzy search query %q: %v", reqs[1].Query, err)
	}
	if fuzzyParams.Has("moviehash") {
		t.Errorf("fuzzy search query %q must not include moviehash", reqs[1].Query)
	}
	wantParams := map[string]string{"query": "Arrival", "year": "2016", "languages": "en"}
	for k, want := range wantParams {
		if got := fuzzyParams.Get(k); got != want {
			t.Errorf("fuzzy search param %q = %q, want %q", k, got, want)
		}
	}
}

func TestSearch_NoPath_SkipsHashSearchGoesStraightToFuzzy(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 333),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != "333" {
		t.Fatalf("candidates = %+v, want one candidate with ID 333", candidates)
	}
	if reqs := mock.requestsFor(http.MethodGet, "/subtitles"); len(reqs) != 1 {
		t.Errorf("got %d /subtitles requests, want exactly 1 (no video path to hash)", len(reqs))
	}
}

func TestSearch_MultipleFilesPerItem_YieldsOneCandidateEach(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 1, 2),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	query := provider.Query{Title: "Arrival", Year: 2016, Path: writeVideoFile(t)}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2 (one per file)", len(candidates))
	}
}

// --- Download ---

func TestDownload_TwoStepSignedURLFlow(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-token"}))
	mock.on(http.MethodPost, "/download", jsonHandler(http.StatusOK, map[string]any{
		"link": mockServerLink(mock, "/signed/sub.srt"),
	}))
	mock.on(http.MethodGet, "/signed/sub.srt", rawHandler(http.StatusOK, "1\n00:00:01,000 --> 00:00:02,000\nHello\n"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	data, err := p.Download(context.Background(), domain.Candidate{ID: "111"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if want := "1\n00:00:01,000 --> 00:00:02,000\nHello\n"; string(data) != want {
		t.Errorf("Download() = %q, want %q", data, want)
	}

	downloadReqs := mock.requestsFor(http.MethodPost, "/download")
	if len(downloadReqs) != 1 {
		t.Fatalf("got %d /download requests, want 1", len(downloadReqs))
	}
	if got := bearerToken(t, downloadReqs[0]); got != "jwt-token" {
		t.Errorf("Authorization bearer = %q, want %q", got, "jwt-token")
	}
	if string(downloadReqs[0].Body) != `{"file_id":111}` {
		t.Errorf("download request body = %q, want %q", downloadReqs[0].Body, `{"file_id":111}`)
	}
}

func TestDownload_InvalidCandidateID(t *testing.T) {
	mock := newMockServer(t)
	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "not-a-number"}); err == nil {
		t.Fatal("expected an error for a non-numeric candidate ID")
	}
}

func TestDownload_CachesJWTAcrossCalls(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-token"}))
	for range 2 {
		mock.on(http.MethodPost, "/download", jsonHandler(http.StatusOK, map[string]any{
			"link": mockServerLink(mock, "/signed/sub.srt"),
		}))
		mock.on(http.MethodGet, "/signed/sub.srt", rawHandler(http.StatusOK, "content"))
	}

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "1"}); err != nil {
		t.Fatalf("first Download() error = %v", err)
	}
	if _, err := p.Download(context.Background(), domain.Candidate{ID: "2"}); err != nil {
		t.Fatalf("second Download() error = %v", err)
	}

	if reqs := mock.requestsFor(http.MethodPost, "/login"); len(reqs) != 1 {
		t.Errorf("got %d /login requests across two Downloads, want 1 (JWT should be cached)", len(reqs))
	}
}

func TestDownload_ReLoginsAfterTokenExpires(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-1"}))
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-2"}))
	for range 2 {
		mock.on(http.MethodPost, "/download", jsonHandler(http.StatusOK, map[string]any{
			"link": mockServerLink(mock, "/signed/sub.srt"),
		}))
		mock.on(http.MethodGet, "/signed/sub.srt", rawHandler(http.StatusOK, "content"))
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{}
	p, err := opensubtitles.New(opensubtitles.Config{
		Secrets: testSecrets(),
		BaseURL: mock.URL(),
		Clock:   clock,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "1"}); err != nil {
		t.Fatalf("first Download() error = %v", err)
	}
	now = now.Add(25 * time.Hour) // past the 24h token TTL
	if _, err := p.Download(context.Background(), domain.Candidate{ID: "2"}); err != nil {
		t.Fatalf("second Download() error = %v", err)
	}

	if reqs := mock.requestsFor(http.MethodPost, "/login"); len(reqs) != 2 {
		t.Errorf("got %d /login requests, want 2 (token should have expired)", len(reqs))
	}
}

func TestDownload_QuotaExhausted(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-token"}))
	mock.on(http.MethodPost, "/download", jsonHandler(http.StatusUnauthorized, map[string]any{
		"message":        "You have downloaded your allowed 5 subtitles for 24h.",
		"reset_time_utc": "2026-01-02T00:00:00Z",
	}))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "1"})

	var quotaErr *opensubtitles.QuotaExhaustedError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("Download() error = %v, want a *QuotaExhaustedError", err)
	}
	if quotaErr.ResetAtUTC != "2026-01-02T00:00:00Z" {
		t.Errorf("ResetAtUTC = %q, want %q", quotaErr.ResetAtUTC, "2026-01-02T00:00:00Z")
	}

	// A quota-exhausted 401 is terminal: no retry attempts.
	if reqs := mock.requestsFor(http.MethodPost, "/download"); len(reqs) != 1 {
		t.Errorf("got %d /download requests, want 1 (no retry on quota exhaustion)", len(reqs))
	}
}

func TestDownload_AuthFailure_InvalidatesTokenAndReLogsInNextCall(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-1"}))
	mock.on(http.MethodPost, "/download", jsonHandler(http.StatusUnauthorized, map[string]any{
		"message": "Invalid token",
	}))
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusOK, map[string]any{"token": "jwt-2"}))
	mock.on(http.MethodPost, "/download", jsonHandler(http.StatusOK, map[string]any{
		"link": mockServerLink(mock, "/signed/sub.srt"),
	}))
	mock.on(http.MethodGet, "/signed/sub.srt", rawHandler(http.StatusOK, "content"))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "1"})
	var authErr *opensubtitles.AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("first Download() error = %v, want a *AuthenticationError", err)
	}

	if _, err := p.Download(context.Background(), domain.Candidate{ID: "2"}); err != nil {
		t.Fatalf("second Download() error = %v", err)
	}
	if reqs := mock.requestsFor(http.MethodPost, "/login"); len(reqs) != 2 {
		t.Errorf("got %d /login requests, want 2 (invalidated token forces a re-login)", len(reqs))
	}
}

func TestLogin_AuthFailure_IsTerminalNotRetried(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodPost, "/login", jsonHandler(http.StatusUnauthorized, map[string]any{"message": "bad credentials"}))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	_, err := p.Download(context.Background(), domain.Candidate{ID: "1"})
	var authErr *opensubtitles.AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("Download() error = %v, want a *AuthenticationError", err)
	}
	if reqs := mock.requestsFor(http.MethodPost, "/login"); len(reqs) != 1 {
		t.Errorf("got %d /login requests, want 1 (401 must not be retried)", len(reqs))
	}
	if len(clock.delays) != 0 {
		t.Errorf("recorded delays = %v, want none", clock.delays)
	}
}

// --- Retry/backoff integration (per-task retry.Executor + queue pacer) ---

func TestSearch_RetriesOn429ThenSucceeds(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusTooManyRequests, map[string]any{"message": "slow down"}))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse(
		searchItem("Arrival.2016.1080p.BluRay.x264-GROUP", "Arrival", 2016, 0, 0, 1),
	)))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	query := provider.Query{Title: "Arrival", Year: 2016}
	candidates, err := p.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}

	// retry.Executor's own per-task backoff delay before the retried
	// attempt: DefaultPolicy's 1s base delay under full jitter, so anywhere
	// in [0, 1s].
	if len(clock.delays) == 0 {
		t.Fatal("expected at least one recorded delay from the retry executor")
	}
	if clock.delays[0] < 0 || clock.delays[0] > time.Second {
		t.Errorf("first recorded delay = %v, want in [0, 1s] (retry.Executor's jittered base delay)", clock.delays[0])
	}
}

func TestSearch_HonorsRetryAfterHintOverExecutorBackoff(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", withRetryAfter(5, jsonHandler(http.StatusTooManyRequests, map[string]any{"message": "slow down"})))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if len(clock.delays) == 0 || clock.delays[0] != 5*time.Second {
		t.Errorf("recorded delays = %v, want first delay of 5s (the Retry-After hint)", clock.delays)
	}
}

func TestSearch_5xxIsRetried(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusServiceUnavailable, map[string]any{"message": "down"}))
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusOK, searchResponse()))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestSearch_RetriesExhausted_ReturnsError(t *testing.T) {
	mock := newMockServer(t)
	for range 4 { // initial + 3 retries, per retry.DefaultPolicy
		mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusTooManyRequests, map[string]any{"message": "slow down"}))
	}

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016}); err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
}

func TestSearch_NonRetryableClientError_FailsImmediately(t *testing.T) {
	mock := newMockServer(t)
	mock.on(http.MethodGet, "/subtitles", jsonHandler(http.StatusBadRequest, map[string]any{"message": "bad query"}))

	clock := &fakeClock{}
	p := newTestProvider(t, mock, clock)

	if _, err := p.Search(context.Background(), provider.Query{Title: "Arrival", Year: 2016}); err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if len(clock.delays) != 0 {
		t.Errorf("recorded delays = %v, want none (400 is terminal, not retried)", clock.delays)
	}
}

// mockServerLink builds an absolute URL against mock's own server, so the
// download flow's second-step GET stays within the same test double.
func mockServerLink(mock *mockServer, path string) string {
	return mock.URL() + path
}

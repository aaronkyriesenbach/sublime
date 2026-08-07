// Package opensubtitles is Sublime's real Provider (internal/provider)
// implementation against OpenSubtitles.com's REST v1 API. See CONTEXT.md's
// Provider entry and issue #13's research notes for the auth model, quota
// behavior, and rate-limit posture this package implements: an Api-Key
// header plus a cached 24h Bearer JWT (auth.go), a `moviehash` search
// parameter computed independently from Sublime's Content Hash
// (moviehash.go), a two-step signed-URL download that consumes the daily
// quota (client.go), and a serial, adaptively-paced outgoing queue via
// internal/retry's Pacer layered on top of per-task retry engine.
package opensubtitles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

const (
	defaultBaseURL        = "https://api.opensubtitles.com/api/v1"
	defaultUserAgent      = "Sublime v1"
	defaultPace           = time.Second
	defaultMaxDelay       = 60 * time.Second
	defaultDecayThreshold = 10
)

// Config configures a Provider. Secrets is required; every other field has
// a project-agreed default (see issue #13/#25's resolutions) and only
// needs setting to override that default, e.g. in tests.
type Config struct {
	Secrets config.OpenSubtitlesSecrets

	// BaseURL overrides the OpenSubtitles API's base URL; defaults to
	// defaultBaseURL. Point it at an httptest.Server's URL in tests.
	BaseURL string

	// UserAgent overrides the User-Agent header OpenSubtitles' own Best
	// Practices guidance expects (`<AppName> v<Version>`).
	UserAgent string

	// HTTPClient overrides the *http.Client used for outgoing requests;
	// defaults to http.DefaultClient.
	HTTPClient *http.Client

	// Pace overrides the pacer's steady-state minimum spacing between
	// requests; defaults to 1 second (issue #25's resolution).
	Pace time.Duration

	// Clock overrides the retry.Clock the pacer and retry.Executor use to
	// await delays; defaults to retry.RealClock{}. Tests inject a fake
	// that records requested delays instead of sleeping.
	Clock retry.Clock

	// Now overrides the wall clock used for JWT-expiry bookkeeping, Retry-
	// After date parsing, and quota-suspension bookkeeping; defaults to
	// time.Now.
	Now func() time.Time

	// Logger receives the Provider's own quota-suspension transition logs
	// (entering and resuming) — see Provider.logger. Defaults to
	// slog.Default() if nil, mirroring Pipeline.Logger's convention.
	Logger *slog.Logger
}

// Provider is Sublime's real OpenSubtitles Provider.
type Provider struct {
	client   *client
	auth     *authenticator
	executor *retry.Executor
	now      func() time.Time
	log      *slog.Logger

	mu             sync.Mutex
	suspendedUntil time.Time
}

var _ provider.Provider = (*Provider)(nil)

// New constructs a Provider from cfg. It performs no network calls —
// authentication happens lazily on first Download.
func New(cfg Config) (*Provider, error) {
	if cfg.Secrets.APIKey == "" {
		return nil, errors.New("opensubtitles: Secrets.APIKey is required")
	}
	if cfg.Secrets.Username == "" || cfg.Secrets.Password == "" {
		return nil, errors.New("opensubtitles: Secrets.Username and Secrets.Password are required")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	pace := cfg.Pace
	if pace <= 0 {
		pace = defaultPace
	}
	clock := cfg.Clock
	if clock == nil {
		clock = retry.RealClock{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	pacerCfg := retry.PacerConfig{Pace: pace, MaxDelay: defaultMaxDelay, DecayThreshold: defaultDecayThreshold}
	c := &client{
		baseURL:    baseURL,
		apiKey:     cfg.Secrets.APIKey,
		userAgent:  userAgent,
		httpClient: httpClient,
		pacer:      retry.NewPacer(pacerCfg, clock),
		now:        now,
	}
	executor := retry.NewExecutor(clock)

	return &Provider{
		client:   c,
		executor: executor,
		auth:     newAuthenticator(c, executor, cfg.Secrets.Username, cfg.Secrets.Password, now),
		now:      now,
		log:      cfg.Logger,
	}, nil
}

// logger returns p.log, or slog.Default() if it isn't set.
func (p *Provider) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// suspendedError returns the quota-exhaustion signal to short-circuit the
// caller with if p is still suspended as of now, or nil if p is free to
// make a real request. Once the suspension's resume time has passed, it
// clears the suspension and logs the resume transition before returning
// nil — so the very next Search/Download call after that point issues a
// real request again.
func (p *Provider) suspendedError() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.suspendedUntil.IsZero() {
		return nil
	}
	if p.now().Before(p.suspendedUntil) {
		return &provider.QuotaExhaustedError{ResumeAt: p.suspendedUntil}
	}

	resumedAt := p.suspendedUntil
	p.suspendedUntil = time.Time{}
	p.logger().Info("opensubtitles: resuming after quota suspension", "resume_at", resumedAt)
	return nil
}

// Suspension reports p's current quota-suspension state: resumeAt is when
// p will resume issuing real requests, and suspended is whether p is
// currently in that state as of now. It implements the optional
// suspension-reporting capability internal/pipeline's production wiring
// type-asserts for (see pipeline.NewProduction), letting GET /status
// surface a Provider's Suspended state (issue #58) without the api
// package needing to know about this concrete type. A resumeAt that has
// already passed is reported as not suspended, even though
// suspendedError hasn't cleared p.suspendedUntil yet — that happens
// lazily on the next real Search/Download call.
func (p *Provider) Suspension() (resumeAt time.Time, suspended bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.suspendedUntil.IsZero() || !p.now().Before(p.suspendedUntil) {
		return time.Time{}, false
	}
	return p.suspendedUntil, true
}

// suspend enters the Suspended state until resumeAt and returns the
// provider.QuotaExhaustedError callers should see, wrapping cause (the
// OpenSubtitles-specific error that revealed the exhaustion) so it stays
// inspectable via errors.As/Is.
func (p *Provider) suspend(cause error, resumeAt time.Time) error {
	p.mu.Lock()
	p.suspendedUntil = resumeAt
	p.mu.Unlock()

	p.logger().Warn("opensubtitles: suspending due to quota exhaustion", "cause", cause, "resume_at", resumeAt)
	return &provider.QuotaExhaustedError{ResumeAt: resumeAt, Cause: cause}
}

// quotaOrWrap inspects err for either quota-exhaustion response shape
// (quotaExhaustion) and, if found, suspends p and returns the
// provider.QuotaExhaustedError to surface; otherwise it returns ok=false so
// the caller applies its own (non-quota) error handling.
func (p *Provider) quotaOrWrap(err error) (wrapped error, ok bool) {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	resumeAt, exhausted := quotaExhaustion(apiErr)
	if !exhausted {
		return nil, false
	}
	return p.suspend(apiErr, resumeAt), true
}

// Search implements provider.Provider. It tries OpenSubtitles' own
// moviehash search first (which, per the Matching & scoring decision,
// dominates when present) and returns those Candidates immediately without
// spending a second request on the fuzzy fallback; only when the hash
// search finds nothing does it fall back to a fuzzy, metadata-based
// search.
func (p *Provider) Search(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
	if err := p.suspendedError(); err != nil {
		return nil, err
	}
	hashCandidates, err := p.searchByHash(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(hashCandidates) > 0 {
		return hashCandidates, nil
	}
	return p.searchFuzzy(ctx, query)
}

func (p *Provider) searchByHash(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
	if query.Path == "" {
		return nil, nil
	}

	hash, err := ComputeMovieHash(query.Path)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: computing moviehash for %q: %w", query.Path, err)
	}

	params := searchParams(query)
	params.Set("moviehash", hash)
	params.Set("moviehash_match", "only")

	resp, err := p.searchRequest(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: hash-match search: %w", err)
	}
	return candidatesFromResponse(resp, true), nil
}

func (p *Provider) searchFuzzy(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
	params := searchParams(query)
	if query.Title != "" {
		params.Set("query", query.Title)
	}
	if query.Year != 0 {
		params.Set("year", strconv.Itoa(query.Year))
	}
	if query.Season != 0 {
		params.Set("season_number", strconv.Itoa(query.Season))
	}
	if query.Episode != 0 {
		params.Set("episode_number", strconv.Itoa(query.Episode))
	}

	resp, err := p.searchRequest(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: fuzzy search: %w", err)
	}
	return candidatesFromResponse(resp, false), nil
}

func (p *Provider) searchRequest(ctx context.Context, params url.Values) (searchResponseBody, error) {
	path := "/subtitles?" + params.Encode()
	op := func(ctx context.Context, _ int) (searchResponseBody, error) {
		return doJSON[noRequestBody, searchResponseBody](ctx, p.client, http.MethodGet, path, nil, "")
	}
	resp, err := retry.Do(ctx, p.executor, op)
	if err != nil {
		if wrapped, ok := p.quotaOrWrap(err); ok {
			return searchResponseBody{}, wrapped
		}
		return searchResponseBody{}, err
	}
	return resp, nil
}

// Download implements provider.Provider via OpenSubtitles' two-step
// signed-URL flow: POST /download exchanges candidate's file_id for a
// short-lived link (consuming the daily download quota), then a plain GET
// on that link fetches the subtitle bytes.
func (p *Provider) Download(ctx context.Context, candidate domain.Candidate) ([]byte, error) {
	if err := p.suspendedError(); err != nil {
		return nil, err
	}

	fileID, err := strconv.Atoi(candidate.ID)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: candidate ID %q is not a valid file_id: %w", candidate.ID, err)
	}

	token, err := p.auth.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: authenticating: %w", err)
	}

	body := downloadRequestBody{FileID: fileID}
	op := func(ctx context.Context, _ int) (downloadResponseBody, error) {
		return doJSON[downloadRequestBody, downloadResponseBody](ctx, p.client, http.MethodPost, "/download", &body, token)
	}

	resp, err := retry.Do(ctx, p.executor, op)
	if err != nil {
		if wrapped, ok := p.quotaOrWrap(err); ok {
			return nil, wrapped
		}
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			p.auth.invalidate()
			return nil, &AuthenticationError{Message: apiErr.Message}
		}
		return nil, fmt.Errorf("opensubtitles: requesting download link: %w", err)
	}

	getOp := func(ctx context.Context, _ int) ([]byte, error) {
		return p.client.getRaw(ctx, resp.Link)
	}
	data, err := retry.Do(ctx, p.executor, getOp)
	if err != nil {
		if wrapped, ok := p.quotaOrWrap(err); ok {
			return nil, wrapped
		}
		return nil, fmt.Errorf("opensubtitles: fetching subtitle content: %w", err)
	}
	return data, nil
}

// searchParams builds the query parameters common to both search phases.
func searchParams(query provider.Query) url.Values {
	params := url.Values{}
	if lang := languageParam(query.Language); lang != "" {
		params.Set("languages", lang)
	}
	return params
}

func languageParam(tag language.Tag) string {
	if tag == language.Und {
		return ""
	}
	return strings.ToLower(tag.String())
}

// candidatesFromResponse converts a search response into domain.Candidates,
// tagging every one with hashMatch: true only ever comes from a
// moviehash_match=only search, where it's true by construction for every
// result, not from a per-item response field.
func candidatesFromResponse(resp searchResponseBody, hashMatch bool) []domain.Candidate {
	var candidates []domain.Candidate
	for _, item := range resp.Data {
		fd := item.Attributes.FeatureDetails
		source, releaseGroup, resolution, codec := provider.ParseCosmetics(item.Attributes.Release)

		title := fd.Title
		if fd.ParentTitle != "" {
			title = fd.ParentTitle
		}

		for _, file := range item.Attributes.Files {
			if file.FileID == 0 {
				continue
			}
			candidates = append(candidates, domain.Candidate{
				ID:           strconv.Itoa(file.FileID),
				Title:        title,
				Year:         fd.Year,
				Season:       fd.SeasonNumber,
				Episode:      fd.EpisodeNumber,
				Source:       source,
				ReleaseGroup: releaseGroup,
				Resolution:   resolution,
				Codec:        codec,
				HashMatch:    hashMatch,
			})
		}
	}
	return candidates
}

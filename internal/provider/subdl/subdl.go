// Package subdl is Sublime's second real Provider (internal/provider)
// implementation, against SubDL's REST API. See CONTEXT.md's Provider entry
// and issue #66's epic for the rationale: an independent subtitle database
// Sublime falls back to when OpenSubtitles has no Candidate, or is
// currently Suspended for quota exhaustion.
//
// This package covers the happy path (issue #69): mapping a provider.Query
// to SubDL's /subtitles search params, converting results into
// domain.Candidates via the shared cosmetics parser
// (internal/provider.ParseCosmetics), and downloading a Candidate's raw
// subtitle bytes via either SubDL's anonymous per-IP path or its
// authenticated/paid path, selected by Config.Paid alone (see ADR 0006) —
// plus quota-exhaustion detection and Provider Suspension (issue #70, ADR
// 0007): the one documented shape, 429 + {"error":"quota_exceeded"}, from
// either Search or an authenticated Download, suspends the Provider until
// its resume time, short-circuiting further Search/Download calls until
// then. The anonymous download path's undocumented rejection shape is
// deliberately excluded from this detection (ADR 0007).
package subdl

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

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

const (
	defaultBaseURL        = "https://api.subdl.com/api/v1"
	defaultUserAgent      = "Sublime v1"
	defaultPace           = time.Second
	defaultMaxDelay       = 60 * time.Second
	defaultDecayThreshold = 10
)

// subdlLanguageOverrides maps a full BCP 47 tag to SubDL's own language
// code for the known regional-variant divergences where a plain lowercase
// ISO 639-1 subtag would collapse a distinct SubDL code (e.g. Brazilian
// Portuguese's "pob", an ISO 639-2 code, not "pt"). Seeded with the one
// confirmed case (issue #69); add further overrides here as they're
// confirmed, not speculatively.
var subdlLanguageOverrides = map[string]string{
	"pt-BR": "pob",
}

// Config configures a Provider. APIKey is required — SubDL requires it
// unconditionally for search, even on a free account (ADR 0006). Every
// other field has a project-agreed default and only needs setting to
// override that default, e.g. in tests.
type Config struct {
	// APIKey is the SubDL account's API key. Always sent on search;
	// attached on Download only when Paid is true (ADR 0006).
	APIKey string

	// Paid declares whether this SubDL account is on a paid plan: when
	// true, Download attaches APIKey and draws from the account's paid
	// download quota; when false (the default), APIKey is omitted and
	// Download uses SubDL's anonymous per-IP quota instead. This is a
	// config-declared fact, not something Sublime infers at runtime (ADR
	// 0006).
	Paid bool

	// BaseURL overrides SubDL's API's base URL; defaults to defaultBaseURL.
	// Point it at an httptest.Server's URL in tests.
	BaseURL string

	// UserAgent overrides the User-Agent header sent on every request.
	UserAgent string

	// HTTPClient overrides the *http.Client used for outgoing requests;
	// defaults to http.DefaultClient.
	HTTPClient *http.Client

	// Pace overrides the pacer's steady-state minimum spacing between
	// requests; defaults to 1 second, mirroring opensubtitles.Config.Pace.
	Pace time.Duration

	// Clock overrides the retry.Clock the pacer and retry.Executor use to
	// await delays; defaults to retry.RealClock{}. Tests inject a fake
	// that records requested delays instead of sleeping.
	Clock retry.Clock

	// Now overrides the wall clock used for quota-suspension bookkeeping;
	// defaults to time.Now.
	Now func() time.Time

	// Logger receives the Provider's own quota-suspension transition logs
	// (entering and resuming); defaults to slog.Default() if nil,
	// mirroring opensubtitles.Config.Logger's convention.
	Logger *slog.Logger
}

// Provider is Sublime's real SubDL Provider.
type Provider struct {
	client   *client
	executor *retry.Executor
	apiKey   string
	paid     bool
	now      func() time.Time
	log      *slog.Logger

	mu             sync.Mutex
	suspendedUntil time.Time
}

var _ provider.Provider = (*Provider)(nil)

// New constructs a Provider from cfg. It performs no network calls.
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("subdl: Config.APIKey is required")
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
		userAgent:  userAgent,
		httpClient: httpClient,
		pacer:      retry.NewPacer(pacerCfg, clock),
		now:        now,
	}

	return &Provider{
		client:   c,
		executor: retry.NewExecutor(clock),
		apiKey:   cfg.APIKey,
		paid:     cfg.Paid,
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
// nil, mirroring opensubtitles.Provider.suspendedError.
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
	p.logger().Info("subdl: resuming after quota suspension", "resume_at", resumedAt)
	return nil
}

// Suspension reports p's current quota-suspension state: resumeAt is when
// p will resume issuing real requests, and suspended is whether p is
// currently in that state as of now. It implements the optional
// suspension-reporting capability internal/pipeline's production wiring
// type-asserts for (see pipeline.NewProduction's suspensionReporter),
// mirroring opensubtitles.Provider.Suspension.
func (p *Provider) Suspension() (resumeAt time.Time, suspended bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.suspendedUntil.IsZero() || !p.now().Before(p.suspendedUntil) {
		return time.Time{}, false
	}
	return p.suspendedUntil, true
}

// suspend enters the Suspended state until resumeAt and returns the
// provider.QuotaExhaustedError callers should see, wrapping cause so it
// stays inspectable via errors.As/Is.
func (p *Provider) suspend(cause error, resumeAt time.Time) error {
	p.mu.Lock()
	p.suspendedUntil = resumeAt
	p.mu.Unlock()

	p.logger().Warn("subdl: suspending due to quota exhaustion", "cause", cause, "resume_at", resumeAt)
	return &provider.QuotaExhaustedError{ResumeAt: resumeAt, Cause: cause}
}

// quotaOrWrap inspects err for SubDL's one recognized quota-exhaustion
// shape (quotaExhaustion) and, if found, suspends p and returns the
// provider.QuotaExhaustedError to surface; otherwise it returns ok=false so
// the caller applies its own (non-quota) error handling. Callers on the
// anonymous download path must not call this at all (ADR 0007).
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

// Search implements provider.Provider. It maps query to SubDL's
// /subtitles search params — film_name, year, season_number,
// episode_number, and a translated languages value — never full_season or
// unpack, since SubDL season-pack results aren't supported (issue #69).
func (p *Provider) Search(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
	if err := p.suspendedError(); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("api_key", p.apiKey)
	if query.Title != "" {
		params.Set("film_name", query.Title)
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
	if lang := languageParam(query.Language); lang != "" {
		params.Set("languages", lang)
	}

	path := "/subtitles?" + params.Encode()
	op := func(ctx context.Context, _ int) (searchResponseBody, error) {
		var out searchResponseBody
		err := p.client.getJSON(ctx, path, &out)
		return out, err
	}
	resp, err := retry.Do(ctx, p.executor, op)
	if err != nil {
		if wrapped, ok := p.quotaOrWrap(err); ok {
			return nil, wrapped
		}
		return nil, fmt.Errorf("subdl: search: %w", err)
	}
	return candidatesFromResponse(resp), nil
}

// Download implements provider.Provider. It attaches APIKey (SubDL's
// authenticated/paid download path) only when Config.Paid is true;
// otherwise the key is omitted entirely, using SubDL's anonymous per-IP
// download path (ADR 0006). This choice is fixed by config, never
// runtime-detected or retried across paths. Quota-exhaustion detection
// (quotaOrWrap) only ever applies to the paid path: the anonymous path's
// rejection response has no documented shape and must never be
// pattern-matched into a Provider Suspension (ADR 0007).
func (p *Provider) Download(ctx context.Context, candidate domain.Candidate) ([]byte, error) {
	if err := p.suspendedError(); err != nil {
		return nil, err
	}

	nID, fileNID, err := parseCandidateID(candidate.ID)
	if err != nil {
		return nil, fmt.Errorf("subdl: %w", err)
	}

	params := url.Values{}
	params.Set("n_id", strconv.Itoa(nID))
	params.Set("file_n_id", strconv.Itoa(fileNID))
	if p.paid {
		params.Set("api_key", p.apiKey)
	}

	path := "/download?" + params.Encode()
	op := func(ctx context.Context, _ int) ([]byte, error) {
		return p.client.getRaw(ctx, path)
	}
	data, err := retry.Do(ctx, p.executor, op)
	if err != nil {
		if p.paid {
			if wrapped, ok := p.quotaOrWrap(err); ok {
				return nil, wrapped
			}
		}
		return nil, fmt.Errorf("subdl: download: %w", err)
	}
	return data, nil
}

// languageParam translates tag into SubDL's own language code: an explicit
// override for a known regional-variant divergence (subdlLanguageOverrides)
// takes precedence, falling back to a lowercase ISO 639-1 subtag — mirrors
// opensubtitles.languageParam's shape (an empty string for language.Und,
// otherwise a lowercase code), but resolves to the tag's base subtag rather
// than its full string, since SubDL's codes are plain ISO 639-1 outside the
// override table.
func languageParam(tag language.Tag) string {
	if tag == language.Und {
		return ""
	}
	if override, ok := subdlLanguageOverrides[tag.String()]; ok {
		return override
	}
	base, _ := tag.Base()
	return strings.ToLower(base.String())
}

// candidateID encodes SubDL's n_id/file_n_id pair as a single opaque
// Candidate.ID, round-tripped by parseCandidateID. The encoding itself
// isn't prescribed by SubDL's API (issue #69's noted ambiguity) — a simple
// delimited string is the least machinery that still round-trips exactly.
func candidateID(nID, fileNID int) string {
	return fmt.Sprintf("%d-%d", nID, fileNID)
}

// parseCandidateID reverses candidateID, rejecting any ID not shaped like
// this Provider's own encoding — e.g. a Candidate.ID that actually came
// from a different Provider.
func parseCandidateID(id string) (nID, fileNID int, err error) {
	before, after, ok := strings.Cut(id, "-")
	if !ok {
		return 0, 0, fmt.Errorf("candidate ID %q is not in the expected n_id-file_n_id form", id)
	}
	nID, err = strconv.Atoi(before)
	if err != nil {
		return 0, 0, fmt.Errorf("candidate ID %q: invalid n_id: %w", id, err)
	}
	fileNID, err = strconv.Atoi(after)
	if err != nil {
		return 0, 0, fmt.Errorf("candidate ID %q: invalid file_n_id: %w", id, err)
	}
	return nID, fileNID, nil
}

// candidatesFromResponse converts a search response into domain.Candidates.
// Every one has HashMatch: false — SubDL has no moviehash-equivalent search
// (issue #69) — so results flow through the same fuzzy/cosmetic scoring
// cutoff OpenSubtitles' own non-hash path already uses.
func candidatesFromResponse(resp searchResponseBody) []domain.Candidate {
	var candidates []domain.Candidate
	for _, item := range resp.Subtitles {
		source, releaseGroup, resolution, codec := provider.ParseCosmetics(item.ReleaseName)
		candidates = append(candidates, domain.Candidate{
			ID:           candidateID(item.NID, item.FileNID),
			Title:        item.Name,
			Year:         item.Year,
			Season:       item.SeasonNumber,
			Episode:      item.EpisodeNumber,
			Source:       source,
			ReleaseGroup: releaseGroup,
			Resolution:   resolution,
			Codec:        codec,
			HashMatch:    false,
		})
	}
	return candidates
}

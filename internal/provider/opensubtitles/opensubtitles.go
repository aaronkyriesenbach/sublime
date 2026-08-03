// Package opensubtitles is Sublime's real Provider (internal/provider)
// implementation against OpenSubtitles.com's REST v1 API. See CONTEXT.md's
// Provider entry and issue #13's research notes for the auth model, quota
// behavior, and rate-limit posture this package implements: an Api-Key
// header plus a cached 24h Bearer JWT (auth.go), a `moviehash` search
// parameter computed independently from Sublime's Content Hash
// (moviehash.go), a two-step signed-URL download that consumes the daily
// quota (client.go), and a serial, adaptively-paced outgoing queue
// (pacer.go) layered on top of internal/retry's per-task retry engine.
package opensubtitles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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

	// Now overrides the wall clock used for JWT-expiry bookkeeping and
	// Retry-After date parsing; defaults to time.Now.
	Now func() time.Time
}

// Provider is Sublime's real OpenSubtitles Provider.
type Provider struct {
	client   *client
	auth     *authenticator
	executor *retry.Executor
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

	pacerCfg := pacerConfig{Pace: pace, MaxDelay: defaultMaxDelay, DecayThreshold: defaultDecayThreshold}
	c := &client{
		baseURL:    baseURL,
		apiKey:     cfg.Secrets.APIKey,
		userAgent:  userAgent,
		httpClient: httpClient,
		pacer:      newPacer(pacerCfg, clock),
		now:        now,
	}
	executor := retry.NewExecutor(clock)

	return &Provider{
		client:   c,
		executor: executor,
		auth:     newAuthenticator(c, executor, cfg.Secrets.Username, cfg.Secrets.Password, now),
	}, nil
}

// Search implements provider.Provider. It tries OpenSubtitles' own
// moviehash search first (which, per the Matching & scoring decision,
// dominates when present) and returns those Candidates immediately without
// spending a second request on the fuzzy fallback; only when the hash
// search finds nothing does it fall back to a fuzzy, metadata-based
// search.
func (p *Provider) Search(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
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
	return retry.Do(ctx, p.executor, op)
}

// Download implements provider.Provider via OpenSubtitles' two-step
// signed-URL flow: POST /download exchanges candidate's file_id for a
// short-lived link (consuming the daily download quota), then a plain GET
// on that link fetches the subtitle bytes.
func (p *Provider) Download(ctx context.Context, candidate domain.Candidate) ([]byte, error) {
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
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			if apiErr.ResetTimeUTC != "" {
				return nil, &QuotaExhaustedError{Message: apiErr.Message, ResetAtUTC: apiErr.ResetTimeUTC}
			}
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
		source, releaseGroup, resolution, codec := parseCosmetics(item.Attributes.Release)

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

// Source, resolution, and codec token vocabulary mirrors the filename
// attribute extraction decision (#29): the same subliminal/Bazarr scene-
// release prior art, applied here to OpenSubtitles' own `release` string
// instead of Sublime's video filename. Kept as a small local parser rather
// than reusing scoring.Parse: that parser's contract requires a full
// identity match (title+year) to succeed at all, which OpenSubtitles'
// release string can't always guarantee (e.g. many episode releases omit a
// year) — reusing it here would silently discard cosmetic tags whenever
// that unrelated check fails.
var (
	cosmeticSourceToken     = regexp.MustCompile(`(?i)^(bluray|web-dl|webrip|hdtv|dvdrip)$`)
	cosmeticResolutionToken = regexp.MustCompile(`(?i)^(2160p|1080p|720p|480p)$`)
	cosmeticCodecToken      = regexp.MustCompile(`(?i)^(x264|x265|hevc|av1)$`)
)

// parseCosmetics extracts a Candidate's cosmetic attributes from
// OpenSubtitles' scene-release-style `release` string (e.g.
// "Arrival.2016.1080p.BluRay.x264-GROUP"). Any dimension without a
// recognized token is left empty, matching Sublime's "missing cosmetic
// attribute scores 0" convention (internal/scoring) rather than erroring.
func parseCosmetics(release string) (source, releaseGroup, resolution, codec string) {
	releaseGroup, remaining := extractCosmeticReleaseGroup(release)

	replacer := strings.NewReplacer(".", " ", "_", " ")
	for _, tok := range strings.Fields(replacer.Replace(remaining)) {
		tok = strings.Trim(tok, "()[]{}")
		switch {
		case source == "" && cosmeticSourceToken.MatchString(tok):
			source = tok
		case resolution == "" && cosmeticResolutionToken.MatchString(tok):
			resolution = tok
		case codec == "" && cosmeticCodecToken.MatchString(tok):
			codec = tok
		}
	}
	return source, releaseGroup, resolution, codec
}

func isRecognizedCosmeticTag(token string) bool {
	return cosmeticSourceToken.MatchString(token) || cosmeticResolutionToken.MatchString(token) || cosmeticCodecToken.MatchString(token)
}

// extractCosmeticReleaseGroup splits the last hyphen-delimited token off
// release's final dot-segment as the release group, per scene-release
// convention (e.g. "x264-GROUP") — the same heuristic as scoring's filename
// parser (#29), applied to OpenSubtitles' release string instead of a
// filename.
func extractCosmeticReleaseGroup(release string) (group string, remaining string) {
	prefix := ""
	lastSeg := release
	if i := strings.LastIndex(release, "."); i != -1 {
		prefix = release[:i+1]
		lastSeg = release[i+1:]
	}

	if !strings.Contains(lastSeg, "-") || strings.Contains(lastSeg, " ") || isRecognizedCosmeticTag(lastSeg) {
		return "", release
	}

	i := strings.LastIndex(lastSeg, "-")
	segPrefix, segSuffix := lastSeg[:i], lastSeg[i+1:]
	if isRecognizedCosmeticTag(segSuffix) {
		return "", release
	}

	return segSuffix, prefix + segPrefix
}

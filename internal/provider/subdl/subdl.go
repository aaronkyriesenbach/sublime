// Package subdl is Sublime's second real Provider (internal/provider)
// implementation, against SubDL's REST API. See CONTEXT.md's Provider entry
// and issue #66's epic for the rationale: an independent subtitle database
// Sublime falls back to when OpenSubtitles has no Candidate, or is
// currently Suspended for quota exhaustion.
//
// This package covers only the happy path (issue #69): mapping a
// provider.Query to SubDL's /subtitles search params, converting results
// into domain.Candidates via the shared cosmetics parser
// (internal/provider.ParseCosmetics), and downloading a Candidate's raw
// subtitle bytes via either SubDL's anonymous per-IP path or its
// authenticated/paid path, selected by Config.Paid alone (see ADR 0006).
// Quota-exhaustion detection and Provider Suspension (ADR 0007) are a
// separate, later ticket (issue #70).
package subdl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
}

// Provider is Sublime's real SubDL Provider.
type Provider struct {
	client   *client
	executor *retry.Executor
	apiKey   string
	paid     bool
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

	pacerCfg := retry.PacerConfig{Pace: pace, MaxDelay: defaultMaxDelay, DecayThreshold: defaultDecayThreshold}
	c := &client{
		baseURL:    baseURL,
		userAgent:  userAgent,
		httpClient: httpClient,
		pacer:      retry.NewPacer(pacerCfg, clock),
	}

	return &Provider{
		client:   c,
		executor: retry.NewExecutor(clock),
		apiKey:   cfg.APIKey,
		paid:     cfg.Paid,
	}, nil
}

// Search implements provider.Provider. It maps query to SubDL's
// /subtitles search params — film_name, year, season_number,
// episode_number, and a translated languages value — never full_season or
// unpack, since SubDL season-pack results aren't supported (issue #69).
func (p *Provider) Search(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
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
		return nil, fmt.Errorf("subdl: search: %w", err)
	}
	return candidatesFromResponse(resp), nil
}

// Download implements provider.Provider. It attaches APIKey (SubDL's
// authenticated/paid download path) only when Config.Paid is true;
// otherwise the key is omitted entirely, using SubDL's anonymous per-IP
// download path (ADR 0006). This choice is fixed by config, never
// runtime-detected or retried across paths.
func (p *Provider) Download(ctx context.Context, candidate domain.Candidate) ([]byte, error) {
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

package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// apiError represents a non-2xx JSON error response from the OpenSubtitles
// API. ResetTimeUTC is only populated on the 401-shaped quota-exhaustion
// response — it is what lets callers distinguish that from a genuine
// authentication failure without depending on the response's English
// message text (issue #13's research: both are plain 401s with an
// "ordinary-looking" body). ObservedAt is the response's own client-clock
// timestamp, kept for quotaExhaustion's fallback-cooldown calculation.
type apiError struct {
	StatusCode   int
	Message      string
	ResetTimeUTC string
	ObservedAt   time.Time
}

func (e *apiError) Error() string {
	return fmt.Sprintf("opensubtitles: request failed with status %d: %s", e.StatusCode, e.Message)
}

// AuthenticationError indicates OpenSubtitles rejected the request's
// credentials (Api-Key or the login username/password). It is always
// terminal: per issue #13's research, a 401 here means stop retrying with
// the same credentials rather than backing off and retrying.
type AuthenticationError struct {
	Message string
}

func (e *AuthenticationError) Error() string {
	return fmt.Sprintf("opensubtitles: authentication failed: %s", e.Message)
}

// client is the low-level HTTP transport for the OpenSubtitles API: request
// building, response classification, and wire-format JSON, with every
// outgoing request routed through retry.Pacer.
type client struct {
	baseURL    string
	apiKey     string
	userAgent  string
	httpClient *http.Client
	pacer      *retry.Pacer
	now        func() time.Time
}

func (c *client) newRequest(ctx context.Context, method, path string, body any, bearer string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("opensubtitles: encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: building request: %w", err)
	}

	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req, nil
}

// do sends a single JSON request through the retry.Pacer and decodes a
// successful response into out (ignored if nil). It is an internal
// plumbing helper: doJSON is the typed entry point every caller outside
// this file actually uses. body and out are any here only because that is
// encoding/json's own Marshal/Unmarshal contract (as in the standard
// library, there is no way to call them without it) — doJSON's type
// parameters are what keep every real call site fully typed.
func (c *client) do(ctx context.Context, method, path string, body any, bearer string, out any) error {
	return c.pacer.Do(ctx, func(ctx context.Context) (retry.Outcome, error) {
		req, err := c.newRequest(ctx, method, path, body, bearer)
		if err != nil {
			return retry.Outcome{}, err
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return retry.Outcome{}, err
		}
		defer func() { _ = resp.Body.Close() }()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return retry.Outcome{Responded: true}, fmt.Errorf("opensubtitles: reading response from %s: %w", path, err)
		}

		oc, classifyErr := classifyResponse(resp.StatusCode, data, resp.Header, c.now())
		if classifyErr != nil {
			return oc, classifyErr
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return oc, fmt.Errorf("opensubtitles: decoding response from %s: %w", path, err)
			}
		}
		return oc, nil
	})
}

// doJSON is the typed entry point for a single JSON request: body is a
// pointer to the request's own wire-format type (nil for a bodyless
// request, e.g. a GET search), and the response is decoded into a fresh
// Resp. Kept as a free function, not a method, since Go methods can't
// carry their own type parameters.
func doJSON[Req any, Resp any](ctx context.Context, c *client, method, path string, body *Req, bearer string) (Resp, error) {
	var out Resp

	// body is a concrete *Req here, so this nil check is unambiguous;
	// converting straight to `any` first would instead box a non-nil
	// interface holding a nil pointer, which client.do's own `!= nil` check
	// would then wrongly treat as "has a body".
	var bodyArg any
	if body != nil {
		bodyArg = body
	}

	err := c.do(ctx, method, path, bodyArg, bearer, &out)
	return out, err
}

// getRaw fetches path (an absolute URL, e.g. a signed download link)
// through the retry.Pacer and returns its raw response body.
func (c *client) getRaw(ctx context.Context, absoluteURL string) ([]byte, error) {
	var data []byte
	err := c.pacer.Do(ctx, func(ctx context.Context) (retry.Outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, absoluteURL, nil)
		if err != nil {
			return retry.Outcome{}, err
		}
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return retry.Outcome{}, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return retry.Outcome{Responded: true}, fmt.Errorf("opensubtitles: reading downloaded subtitle: %w", err)
		}

		oc, classifyErr := classifyResponse(resp.StatusCode, body, resp.Header, c.now())
		if classifyErr != nil {
			return oc, classifyErr
		}
		data = body
		return oc, nil
	})
	return data, err
}

// classifyResponse turns a completed HTTP response into the retry.Pacer outcome
// to record and the error the caller should see: nil on 2xx, a
// retry.TransientError on 429/5xx (adopting a Retry-After delay hint when
// present, per retry.TransientAfter's contract), or a plain terminal
// *apiError otherwise.
func classifyResponse(statusCode int, body []byte, header http.Header, now time.Time) (retry.Outcome, error) {
	if statusCode >= 200 && statusCode < 300 {
		return retry.Outcome{Responded: true}, nil
	}

	retryable := isRetryableStatus(statusCode)
	oc := retry.Outcome{Responded: true, Retryable: retryable}
	apiErr := decodeAPIError(statusCode, body, now)

	if !retryable {
		return oc, apiErr
	}
	if hint, ok := parseRetryAfter(header.Get("Retry-After"), now); ok {
		oc.DelayHint = hint
		return oc, retry.TransientAfter(apiErr, hint)
	}
	return oc, retry.Transient(apiErr)
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
}

// errorBody mirrors the fields of an OpenSubtitles error response that
// matter to Sublime: a human-readable message, and (only present on a
// quota-exhausted 401) the quota's reset time.
type errorBody struct {
	Message      string `json:"message"`
	ResetTimeUTC string `json:"reset_time_utc"`
}

func decodeAPIError(status int, body []byte, observedAt time.Time) *apiError {
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // best-effort: a non-JSON body just yields an empty message
	return &apiError{StatusCode: status, Message: eb.Message, ResetTimeUTC: eb.ResetTimeUTC, ObservedAt: observedAt}
}

// quotaFallbackCooldown is the suspension length used when a quota-
// exhaustion response carries no resume time Sublime can parse (issue
// #56's fallback resolution).
const quotaFallbackCooldown = time.Hour

// embeddedResetTimePattern extracts the reset timestamp OpenSubtitles
// embeds in a 406 response's free-text message, e.g. "...Your quota will
// be renewed in 00 hours and 57 minutes (2026-08-05 23:59:59 UTC)" — the
// real-world quota-exhaustion shape (issue #56), distinct from the
// structured 401+reset_time_utc shape decodeAPIError already captures.
var embeddedResetTimePattern = regexp.MustCompile(`\((\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC\)`)

func parseEmbeddedResetTime(message string) (time.Time, bool) {
	match := embeddedResetTimePattern.FindStringSubmatch(message)
	if match == nil {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02 15:04:05", match[1])
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// quotaExhaustion recognizes both response shapes OpenSubtitles uses for
// quota exhaustion (issue #56): a 401 with a structured reset_time_utc
// field, or a 406 with the reset time only embedded in message's free
// text. It returns the resume time to use — parsed from the response when
// possible, otherwise quotaFallbackCooldown from apiErr's own observed
// time — and false if apiErr isn't a quota-exhaustion response at all.
func quotaExhaustion(apiErr *apiError) (time.Time, bool) {
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		if apiErr.ResetTimeUTC == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339, apiErr.ResetTimeUTC); err == nil {
			return t, true
		}
		return apiErr.ObservedAt.Add(quotaFallbackCooldown), true
	case http.StatusNotAcceptable:
		if t, ok := parseEmbeddedResetTime(apiErr.Message); ok {
			return t, true
		}
		return apiErr.ObservedAt.Add(quotaFallbackCooldown), true
	default:
		return time.Time{}, false
	}
}

// parseRetryAfter parses a Retry-After header in either of its two HTTP-
// spec forms (delta-seconds or an HTTP-date), returning ok=false if header
// is empty or in neither form.
func parseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(header); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// --- Wire-format JSON bodies ---

// noRequestBody is doJSON's marker type for a bodyless request (e.g. a GET
// search): always called with a nil *noRequestBody, never a value.
type noRequestBody struct{}

type loginRequestBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponseBody struct {
	Token string `json:"token"`
}

type searchResponseBody struct {
	Data []searchResultItem `json:"data"`
}

type searchResultItem struct {
	Attributes searchAttributes `json:"attributes"`
}

type searchAttributes struct {
	Release        string         `json:"release"`
	FeatureDetails featureDetails `json:"feature_details"`
	Files          []subtitleFile `json:"files"`
}

type featureDetails struct {
	Title         string `json:"title"`
	Year          int    `json:"year"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`

	// ParentTitle is the show's own title for an episode result (e.g.
	// "Community") — Title above is the *episode's* title instead (e.g.
	// "Anthropology 101") and is empty/absent for movies, which have no
	// parent. See candidatesFromResponse: Sublime's filename-derived
	// Info.Title is always the show's title for episodes, never the
	// individual episode's, so scoring must compare against ParentTitle,
	// not Title, whenever it's present.
	ParentTitle string `json:"parent_title"`
}

type subtitleFile struct {
	FileID int `json:"file_id"`
}

type downloadRequestBody struct {
	FileID int `json:"file_id"`
}

type downloadResponseBody struct {
	Link string `json:"link"`
}

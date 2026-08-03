package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// apiError represents a non-2xx JSON error response from the OpenSubtitles
// API. ResetTimeUTC is only populated on the 401-shaped quota-exhaustion
// response (see QuotaExhaustedError) — it is what lets callers distinguish
// that from a genuine authentication failure without depending on the
// response's English message text (issue #13's research: both are plain
// 401s with an "ordinary-looking" body).
type apiError struct {
	StatusCode   int
	Message      string
	ResetTimeUTC string
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

// QuotaExhaustedError indicates OpenSubtitles' daily download quota is
// exhausted for the current window, surfaced as a 401 with a quota-shaped
// body rather than a real auth failure (issue #13's research). ResetAtUTC
// is OpenSubtitles' own reported reset time, passed through verbatim.
type QuotaExhaustedError struct {
	Message    string
	ResetAtUTC string
}

func (e *QuotaExhaustedError) Error() string {
	return fmt.Sprintf("opensubtitles: download quota exhausted: %s", e.Message)
}

// client is the low-level HTTP transport for the OpenSubtitles API: request
// building, response classification, and wire-format JSON, with every
// outgoing request routed through pacer.
type client struct {
	baseURL    string
	apiKey     string
	userAgent  string
	httpClient *http.Client
	pacer      *pacer
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

// do sends a single JSON request through the pacer and decodes a
// successful response into out (ignored if nil). It is an internal
// plumbing helper: doJSON is the typed entry point every caller outside
// this file actually uses. body and out are any here only because that is
// encoding/json's own Marshal/Unmarshal contract (as in the standard
// library, there is no way to call them without it) — doJSON's type
// parameters are what keep every real call site fully typed.
func (c *client) do(ctx context.Context, method, path string, body any, bearer string, out any) error {
	return c.pacer.do(ctx, func(ctx context.Context) (outcome, error) {
		req, err := c.newRequest(ctx, method, path, body, bearer)
		if err != nil {
			return outcome{}, err
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return outcome{}, err
		}
		defer func() { _ = resp.Body.Close() }()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return outcome{responded: true}, fmt.Errorf("opensubtitles: reading response from %s: %w", path, err)
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
// through the pacer and returns its raw response body.
func (c *client) getRaw(ctx context.Context, absoluteURL string) ([]byte, error) {
	var data []byte
	err := c.pacer.do(ctx, func(ctx context.Context) (outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, absoluteURL, nil)
		if err != nil {
			return outcome{}, err
		}
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return outcome{}, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return outcome{responded: true}, fmt.Errorf("opensubtitles: reading downloaded subtitle: %w", err)
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

// classifyResponse turns a completed HTTP response into the pacer outcome
// to record and the error the caller should see: nil on 2xx, a
// retry.TransientError on 429/5xx (adopting a Retry-After delay hint when
// present, per retry.TransientAfter's contract), or a plain terminal
// *apiError otherwise.
func classifyResponse(statusCode int, body []byte, header http.Header, now time.Time) (outcome, error) {
	if statusCode >= 200 && statusCode < 300 {
		return outcome{responded: true}, nil
	}

	retryable := isRetryableStatus(statusCode)
	oc := outcome{responded: true, retryable: retryable}
	apiErr := decodeAPIError(statusCode, body)

	if !retryable {
		return oc, apiErr
	}
	if hint, ok := parseRetryAfter(header.Get("Retry-After"), now); ok {
		oc.delayHint = hint
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

func decodeAPIError(status int, body []byte) *apiError {
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // best-effort: a non-JSON body just yields an empty message
	return &apiError{StatusCode: status, Message: eb.Message, ResetTimeUTC: eb.ResetTimeUTC}
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

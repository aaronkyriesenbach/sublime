package subdl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// apiError represents a non-2xx response from SubDL's API. RateLimitReset
// carries the response's raw X-RateLimit-Reset header value, if any
// (quotaExhaustion's preferred ResumeAt source); ObservedAt is the
// response's own client-clock timestamp, used for the next-UTC-midnight
// fallback when that header is absent or unparseable (ADR 0007).
type apiError struct {
	StatusCode     int
	Message        string
	RateLimitReset string
	ObservedAt     time.Time
}

func (e *apiError) Error() string {
	return fmt.Sprintf("subdl: request failed with status %d: %s", e.StatusCode, e.Message)
}

// client is the low-level HTTP transport for the SubDL API: request
// building, response classification, and wire-format JSON, with every
// outgoing request routed through retry.Pacer. baseURL and downloadBaseURL
// are deliberately separate hosts: SubDL's JSON search API lives at
// api.subdl.com, but its actual subtitle bytes are served from a
// different host, dl.subdl.com (see getRaw and SubDL's "Downloading
// Subtitles" docs) -- a search response's own "url" field is only a path
// relative to that second host, never a full URL.
type client struct {
	baseURL         string
	downloadBaseURL string
	userAgent       string
	httpClient      *http.Client
	pacer           *retry.Pacer
	now             func() time.Time
}

// getJSON issues a GET request for path (relative to baseURL) through the
// Pacer and decodes a successful JSON response into out.
func (c *client) getJSON(ctx context.Context, path string, out any) error {
	return c.pacer.Do(ctx, func(ctx context.Context) (retry.Outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			return retry.Outcome{}, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return retry.Outcome{}, err
		}
		defer func() { _ = resp.Body.Close() }()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return retry.Outcome{Responded: true}, fmt.Errorf("subdl: reading response from %s: %w", path, err)
		}

		oc, classifyErr := classifyResponse(resp.StatusCode, data, resp.Header, c.now())
		if classifyErr != nil {
			return oc, classifyErr
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return oc, fmt.Errorf("subdl: decoding response from %s: %w", path, err)
			}
		}
		return oc, nil
	})
}

// getRaw issues a GET request for path (relative to downloadBaseURL,
// dl.subdl.com by default -- not baseURL, the JSON search host) through
// the Pacer and returns its raw response body, unmodified -- SubDL's
// download endpoint's regular-listing shape actually returns a zip
// archive despite its own docs describing it as raw subtitle bytes;
// unzipping that response is Provider.Download's job (subdl.go's
// extractSubtitleFromZip), not this transport-level method's (issue #80).
func (c *client) getRaw(ctx context.Context, path string) ([]byte, error) {
	var data []byte
	err := c.pacer.Do(ctx, func(ctx context.Context) (retry.Outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.downloadBaseURL+path, nil)
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
			return retry.Outcome{Responded: true}, fmt.Errorf("subdl: reading downloaded subtitle: %w", err)
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

// classifyResponse turns a completed HTTP response into the retry.Pacer
// outcome to record and the error the caller should see: nil on 2xx, a
// terminal *apiError for the recognized quota-exhaustion shape (429 +
// {"error":"quota_exceeded"}, ADR 0007) so it's never silently retried
// away, a retry.TransientError on any other 429/5xx, or a plain terminal
// *apiError otherwise. Turning the quota-exhaustion *apiError into a
// provider.QuotaExhaustedError and Suspending the Provider is
// Provider.quotaOrWrap's job, in subdl.go, not this classification step's.
func classifyResponse(statusCode int, body []byte, header http.Header, now time.Time) (retry.Outcome, error) {
	if statusCode >= 200 && statusCode < 300 {
		return retry.Outcome{Responded: true}, nil
	}

	apiErr := decodeAPIError(statusCode, body, header, now)
	if _, exhausted := quotaExhaustion(apiErr); exhausted {
		return retry.Outcome{Responded: true, Retryable: false}, apiErr
	}

	retryable := isRetryableStatus(statusCode)
	oc := retry.Outcome{Responded: true, Retryable: retryable}
	if !retryable {
		return oc, apiErr
	}
	return oc, retry.Transient(apiErr)
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
}

// errorBody mirrors SubDL's error response shape: a boolean status flag and
// a human-readable error message.
type errorBody struct {
	Status bool   `json:"status"`
	Error  string `json:"error"`
}

func decodeAPIError(status int, body []byte, header http.Header, observedAt time.Time) *apiError {
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // best-effort: a non-JSON body just yields an empty message
	return &apiError{
		StatusCode:     status,
		Message:        eb.Error,
		RateLimitReset: header.Get("X-RateLimit-Reset"),
		ObservedAt:     observedAt,
	}
}

// quotaExhaustion recognizes SubDL's one documented quota-exhaustion shape
// (ADR 0007): a 429 with {"error":"quota_exceeded"}, from either a search
// or an authenticated download request. It returns the resume time to
// use — parsed from the response's X-RateLimit-Reset header (a Unix
// timestamp) when present and parseable, otherwise the next UTC midnight
// after apiErr's own observed time, since SubDL's quotas are documented as
// calendar-day resets — and false if apiErr isn't this shape at all. Any
// other response, including the anonymous download path's undocumented
// rejection shape, is deliberately left unrecognized here.
func quotaExhaustion(apiErr *apiError) (time.Time, bool) {
	if apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Message != "quota_exceeded" {
		return time.Time{}, false
	}
	if t, ok := parseRateLimitReset(apiErr.RateLimitReset); ok {
		return t, true
	}
	return nextUTCMidnight(apiErr.ObservedAt), true
}

// parseRateLimitReset parses header as a Unix timestamp (seconds since the
// epoch), the conventional format for an X-RateLimit-Reset header.
func parseRateLimitReset(header string) (time.Time, bool) {
	if header == "" {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

// nextUTCMidnight returns the next UTC midnight strictly after now, the
// fallback ResumeAt when a quota-exhaustion response carries no reset time
// Sublime can parse (ADR 0007).
func nextUTCMidnight(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

// --- Wire-format JSON bodies ---

// searchResponseBody mirrors SubDL's /subtitles response's two top-level
// arrays. Results identifies the movie/show SubDL matched the query
// against; Subtitles are, per SubDL's own docs, always "an array of
// subtitles for the first movie/TV show in results" -- there is no
// per-subtitle title, year, or n_id/file_n_id field at all (verified
// against SubDL's published API docs and a live response; see
// candidatesFromResponse in subdl.go, which is why both arrays are
// decoded here rather than Subtitles alone).
// TotalPages and CurrentPage are SubDL's own pagination fields (ADR 0010):
// Search walks additional pages, requesting the same query params with
// only page varied, until CurrentPage == TotalPages or a fixed page cap is
// reached, merging every page's Subtitles into one candidate list.
type searchResponseBody struct {
	Results     []searchResultInfo `json:"results"`
	Subtitles   []searchResultItem `json:"subtitles"`
	TotalPages  int                `json:"totalPages"`
	CurrentPage int                `json:"currentPage"`
}

// searchResultInfo mirrors one entry in SubDL's /subtitles response
// "results" array: the movie/show SubDL matched the query against. Every
// Candidate built from one response shares results[0]'s Name/Year (see
// candidatesFromResponse).
type searchResultInfo struct {
	Name string `json:"name"`
	Year int    `json:"year"`
}

// searchResultItem mirrors one entry in SubDL's /subtitles response
// "subtitles" array. URL is the subtitle's download path, relative to
// dl.subdl.com (client.downloadBaseURL), not api.subdl.com -- see
// client.getRaw. Season/Episode are 0 for a movie or a full-season pack
// (SubDL leaves Episode null for those; decoding JSON null into an int
// field is a no-op in Go, so it comes through as the zero value here).
// FullSeason marks a whole-season-pack archive (never itself a
// Candidate); UnpackFiles is its per-file breakdown, populated only when
// Search sent unpack=1 (see candidatesFromResponse, ADR 0011).
type searchResultItem struct {
	ReleaseName string           `json:"release_name"`
	URL         string           `json:"url"`
	Season      int              `json:"season"`
	Episode     int              `json:"episode"`
	FullSeason  bool             `json:"full_season"`
	UnpackFiles []unpackFileItem `json:"unpack_files"`
}

// unpackFileItem mirrors one entry in a full-season pack's "unpack_files"
// breakdown: Name/ReleaseName feed internal/media.Classify to resolve the
// file's episode identity, and URL becomes the resulting Candidate's ID.
//
// Deliberately no season/episode fields: SubDL's own values there are
// frequently wrong or zeroed in live testing (ADR 0011,
// docs/adr/0011-subdl-unpack-files-matched-via-classify-not-wire-fields.md).
type unpackFileItem struct {
	Name        string `json:"name"`
	ReleaseName string `json:"release_name"`
	URL         string `json:"url"`
}

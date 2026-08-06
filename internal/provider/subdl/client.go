package subdl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// apiError represents a non-2xx response from SubDL's API.
type apiError struct {
	StatusCode int
	Message    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("subdl: request failed with status %d: %s", e.StatusCode, e.Message)
}

// client is the low-level HTTP transport for the SubDL API: request
// building, response classification, and wire-format JSON, with every
// outgoing request routed through retry.Pacer.
type client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	pacer      *retry.Pacer
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

		oc, classifyErr := classifyResponse(resp.StatusCode, data)
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

// getRaw issues a GET request for path (relative to baseURL) through the
// Pacer and returns its raw response body — used for SubDL's download
// endpoint, which returns subtitle bytes directly rather than JSON.
func (c *client) getRaw(ctx context.Context, path string) ([]byte, error) {
	var data []byte
	err := c.pacer.Do(ctx, func(ctx context.Context) (retry.Outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
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

		oc, classifyErr := classifyResponse(resp.StatusCode, body)
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
// retry.TransientError on 429/5xx, or a plain terminal *apiError otherwise.
// Recognizing SubDL's specific quota-exhaustion shape (429 +
// {"error":"quota_exceeded"}) and turning it into a
// provider.QuotaExhaustedError is issue #70's scope, not this one's.
func classifyResponse(statusCode int, body []byte) (retry.Outcome, error) {
	if statusCode >= 200 && statusCode < 300 {
		return retry.Outcome{Responded: true}, nil
	}

	retryable := isRetryableStatus(statusCode)
	oc := retry.Outcome{Responded: true, Retryable: retryable}
	apiErr := decodeAPIError(statusCode, body)

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

func decodeAPIError(status int, body []byte) *apiError {
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // best-effort: a non-JSON body just yields an empty message
	return &apiError{StatusCode: status, Message: eb.Error}
}

// --- Wire-format JSON bodies ---

type searchResponseBody struct {
	Subtitles []searchResultItem `json:"subtitles"`
}

// searchResultItem mirrors one entry in SubDL's /subtitles response.
// NID/FileNID are SubDL's own n_id/file_n_id pair identifying this
// specific subtitle file — see candidateID.
type searchResultItem struct {
	ReleaseName   string `json:"release_name"`
	Name          string `json:"name"`
	Year          int    `json:"year"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	NID           int    `json:"n_id"`
	FileNID       int    `json:"file_n_id"`
}

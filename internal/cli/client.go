package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/api"
)

// apiError is returned by apiClient when the daemon responds with a non-2xx
// status. It carries the uniform error envelope's fields so callers can
// print the daemon's own message rather than a generic "request failed".
type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("sublime: %s", e.Message)
}

// apiClient is a thin HTTP client for internal/api's routes, used by every
// CLI subcommand that talks to a running `sublime serve` daemon.
type apiClient struct {
	baseURL string
	http    *http.Client
}

func newAPIClient(baseURL string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *apiClient) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	return c.do(req, out)
}

func (c *apiClient) post(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *apiClient) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contacting sublime daemon at %s: %w", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from sublime daemon: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env api.ErrorEnvelope
		if jsonErr := json.Unmarshal(data, &env); jsonErr != nil || env.Message == "" {
			return &apiError{StatusCode: resp.StatusCode, Code: "unknown", Message: fmt.Sprintf("unexpected response (status %d)", resp.StatusCode)}
		}
		return &apiError{StatusCode: resp.StatusCode, Code: env.Error, Message: env.Message}
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decoding response from sublime daemon: %w", err)
		}
	}
	return nil
}

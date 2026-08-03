package opensubtitles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// tokenTTL is how long a login token is trusted before re-authenticating.
// Community-reported (not published in OpenSubtitles' own spec) at ~24h;
// see issue #13's research. Login is explicitly called out by OpenSubtitles
// staff as an expensive, separately rate-limited operation, so this cache
// exists specifically to avoid re-logging in per request or per session.
const tokenTTL = 24 * time.Hour

// authenticator manages the OpenSubtitles session's Bearer JWT: it logs in
// only when there is no cached token or the cached one has expired.
type authenticator struct {
	client   *client
	executor *retry.Executor
	username string
	password string
	now      func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newAuthenticator(c *client, executor *retry.Executor, username, password string, now func() time.Time) *authenticator {
	return &authenticator{client: c, executor: executor, username: username, password: password, now: now}
}

// Token returns a cached Bearer JWT, logging in only if there is none yet
// or the cached one has expired.
func (a *authenticator) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != "" && a.now().Before(a.expiresAt) {
		return a.token, nil
	}

	token, err := a.login(ctx)
	if err != nil {
		return "", err
	}

	a.token = token
	a.expiresAt = a.now().Add(tokenTTL)
	return a.token, nil
}

// invalidate drops the cached token, forcing the next Token call to log in
// again. Used when a Bearer token is rejected with a non-quota 401 —
// evidence the cached token itself is no longer valid.
func (a *authenticator) invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = ""
}

// login performs the actual POST /login call, retried through the shared
// retry.Executor for transient (429/5xx) failures. A 401 is never
// transient here regardless of the executor's policy — classifyResponse
// only ever wraps 429/5xx as retry.TransientError — and is translated to
// AuthenticationError: per issue #13's research, a 401 on login means stop
// retrying with these credentials, not back off and retry.
func (a *authenticator) login(ctx context.Context) (string, error) {
	body := loginRequestBody{Username: a.username, Password: a.password}

	op := func(ctx context.Context, _ int) (loginResponseBody, error) {
		return doJSON[loginRequestBody, loginResponseBody](ctx, a.client, http.MethodPost, "/login", &body, "")
	}

	resp, err := retry.Do(ctx, a.executor, op)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			return "", &AuthenticationError{Message: apiErr.Message}
		}
		return "", fmt.Errorf("opensubtitles: login: %w", err)
	}
	return resp.Token, nil
}

package opensubtitles

import (
	"net/http"
	"testing"
	"time"
)

func TestAPIError_Error(t *testing.T) {
	err := &apiError{StatusCode: 429, Message: "slow down"}
	if got, want := err.Error(), "opensubtitles: request failed with status 429: slow down"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestAuthenticationError_Error(t *testing.T) {
	err := &AuthenticationError{Message: "bad credentials"}
	if got, want := err.Error(), "opensubtitles: authentication failed: bad credentials"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestQuotaExhaustedError_Error(t *testing.T) {
	err := &QuotaExhaustedError{Message: "quota exceeded"}
	if got, want := err.Error(), "opensubtitles: download quota exhausted: quota exceeded"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDecodeAPIError_NonJSONBody(t *testing.T) {
	err := decodeAPIError(500, []byte("<html>Internal Server Error</html>"))
	if err.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", err.StatusCode)
	}
	if err.Message != "" {
		t.Errorf("Message = %q, want empty for a non-JSON body", err.Message)
	}
}

func TestParseRetryAfter_DeltaSeconds(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, ok := parseRetryAfter("30", now)
	if !ok {
		t.Fatal("parseRetryAfter() ok = false, want true")
	}
	if got != 30*time.Second {
		t.Errorf("parseRetryAfter() = %v, want 30s", got)
	}
}

func TestParseRetryAfter_NegativeDeltaSecondsClampsToZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, ok := parseRetryAfter("-5", now)
	if !ok {
		t.Fatal("parseRetryAfter() ok = false, want true")
	}
	if got != 0 {
		t.Errorf("parseRetryAfter() = %v, want 0", got)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Minute)
	got, ok := parseRetryAfter(future.UTC().Format(http.TimeFormat), now)
	if !ok {
		t.Fatal("parseRetryAfter() ok = false, want true")
	}
	if got != 2*time.Minute {
		t.Errorf("parseRetryAfter() = %v, want 2m", got)
	}
}

func TestParseRetryAfter_PastHTTPDateClampsToZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-2 * time.Minute)
	got, ok := parseRetryAfter(past.UTC().Format(http.TimeFormat), now)
	if !ok {
		t.Fatal("parseRetryAfter() ok = false, want true")
	}
	if got != 0 {
		t.Errorf("parseRetryAfter() = %v, want 0", got)
	}
}

func TestParseRetryAfter_Empty(t *testing.T) {
	if _, ok := parseRetryAfter("", time.Now()); ok {
		t.Error("parseRetryAfter(\"\") ok = true, want false")
	}
}

func TestParseRetryAfter_Garbage(t *testing.T) {
	if _, ok := parseRetryAfter("not-a-valid-value", time.Now()); ok {
		t.Error("parseRetryAfter() ok = true, want false for an unparseable value")
	}
}

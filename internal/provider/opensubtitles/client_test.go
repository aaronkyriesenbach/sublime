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

func TestDecodeAPIError_NonJSONBody(t *testing.T) {
	err := decodeAPIError(500, []byte("<html>Internal Server Error</html>"), time.Now())
	if err.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", err.StatusCode)
	}
	if err.Message != "" {
		t.Errorf("Message = %q, want empty for a non-JSON body", err.Message)
	}
}

func TestQuotaExhaustion_401WithResetTimeUTC(t *testing.T) {
	apiErr := &apiError{StatusCode: http.StatusUnauthorized, ResetTimeUTC: "2026-01-02T00:00:00Z"}
	resumeAt, ok := quotaExhaustion(apiErr)
	if !ok {
		t.Fatal("quotaExhaustion() ok = false, want true")
	}
	if want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC); !resumeAt.Equal(want) {
		t.Errorf("resumeAt = %v, want %v", resumeAt, want)
	}
}

func TestQuotaExhaustion_401WithoutResetTimeUTC_IsNotQuotaExhaustion(t *testing.T) {
	apiErr := &apiError{StatusCode: http.StatusUnauthorized, Message: "Invalid token"}
	if _, ok := quotaExhaustion(apiErr); ok {
		t.Error("quotaExhaustion() ok = true, want false for a plain auth failure")
	}
}

func TestQuotaExhaustion_406WithEmbeddedResetTime(t *testing.T) {
	apiErr := &apiError{
		StatusCode: http.StatusNotAcceptable,
		Message:    "...Your quota will be renewed in 00 hours and 57 minutes (2026-08-05 23:59:59 UTC)",
	}
	resumeAt, ok := quotaExhaustion(apiErr)
	if !ok {
		t.Fatal("quotaExhaustion() ok = false, want true")
	}
	if want := time.Date(2026, 8, 5, 23, 59, 59, 0, time.UTC); !resumeAt.Equal(want) {
		t.Errorf("resumeAt = %v, want %v", resumeAt, want)
	}
}

func TestQuotaExhaustion_406WithoutParseableResetTime_FallsBackToOneHourCooldown(t *testing.T) {
	observedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	apiErr := &apiError{StatusCode: http.StatusNotAcceptable, Message: "Quota exceeded", ObservedAt: observedAt}
	resumeAt, ok := quotaExhaustion(apiErr)
	if !ok {
		t.Fatal("quotaExhaustion() ok = false, want true")
	}
	if want := observedAt.Add(quotaFallbackCooldown); !resumeAt.Equal(want) {
		t.Errorf("resumeAt = %v, want %v (1h fallback)", resumeAt, want)
	}
}

func TestQuotaExhaustion_OtherStatus_IsNotQuotaExhaustion(t *testing.T) {
	apiErr := &apiError{StatusCode: http.StatusBadRequest, Message: "bad query"}
	if _, ok := quotaExhaustion(apiErr); ok {
		t.Error("quotaExhaustion() ok = true, want false for an unrelated status")
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

package subdl

import (
	"net/http"
	"testing"
	"time"
)

func TestAPIError_Error(t *testing.T) {
	err := &apiError{StatusCode: 429, Message: "slow down"}
	if got, want := err.Error(), "subdl: request failed with status 429: slow down"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDecodeAPIError_NonJSONBody(t *testing.T) {
	err := decodeAPIError(500, []byte("<html>Internal Server Error</html>"), http.Header{}, time.Now())
	if err.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", err.StatusCode)
	}
	if err.Message != "" {
		t.Errorf("Message = %q, want empty for a non-JSON body", err.Message)
	}
}

func TestDecodeAPIError_CapturesRateLimitReset(t *testing.T) {
	header := http.Header{}
	header.Set("X-RateLimit-Reset", "1767225600")
	err := decodeAPIError(429, []byte(`{"error":"quota_exceeded"}`), header, time.Now())
	if err.RateLimitReset != "1767225600" {
		t.Errorf("RateLimitReset = %q, want %q", err.RateLimitReset, "1767225600")
	}
}

func TestQuotaExhaustion_429QuotaExceededWithResetHeader(t *testing.T) {
	apiErr := &apiError{StatusCode: http.StatusTooManyRequests, Message: "quota_exceeded", RateLimitReset: "1767225600"}
	resumeAt, ok := quotaExhaustion(apiErr)
	if !ok {
		t.Fatal("quotaExhaustion() ok = false, want true")
	}
	if want := time.Unix(1767225600, 0).UTC(); !resumeAt.Equal(want) {
		t.Errorf("resumeAt = %v, want %v", resumeAt, want)
	}
}

func TestQuotaExhaustion_429QuotaExceededWithoutResetHeader_FallsBackToNextUTCMidnight(t *testing.T) {
	observedAt := time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)
	apiErr := &apiError{StatusCode: http.StatusTooManyRequests, Message: "quota_exceeded", ObservedAt: observedAt}
	resumeAt, ok := quotaExhaustion(apiErr)
	if !ok {
		t.Fatal("quotaExhaustion() ok = false, want true")
	}
	if want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC); !resumeAt.Equal(want) {
		t.Errorf("resumeAt = %v, want %v (next UTC midnight)", resumeAt, want)
	}
}

func TestQuotaExhaustion_429QuotaExceededWithUnparseableResetHeader_FallsBackToNextUTCMidnight(t *testing.T) {
	observedAt := time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)
	apiErr := &apiError{StatusCode: http.StatusTooManyRequests, Message: "quota_exceeded", RateLimitReset: "not-a-timestamp", ObservedAt: observedAt}
	resumeAt, ok := quotaExhaustion(apiErr)
	if !ok {
		t.Fatal("quotaExhaustion() ok = false, want true")
	}
	if want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC); !resumeAt.Equal(want) {
		t.Errorf("resumeAt = %v, want %v (next UTC midnight)", resumeAt, want)
	}
}

func TestQuotaExhaustion_429WithOtherMessage_IsNotQuotaExhaustion(t *testing.T) {
	apiErr := &apiError{StatusCode: http.StatusTooManyRequests, Message: "slow down"}
	if _, ok := quotaExhaustion(apiErr); ok {
		t.Error("quotaExhaustion() ok = true, want false for a 429 without the quota_exceeded shape")
	}
}

func TestQuotaExhaustion_OtherStatus_IsNotQuotaExhaustion(t *testing.T) {
	apiErr := &apiError{StatusCode: http.StatusBadRequest, Message: "quota_exceeded"}
	if _, ok := quotaExhaustion(apiErr); ok {
		t.Error("quotaExhaustion() ok = true, want false for a non-429 status")
	}
}

func TestNextUTCMidnight(t *testing.T) {
	got := nextUTCMidnight(time.Date(2026, 1, 1, 23, 59, 59, 0, time.UTC))
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextUTCMidnight() = %v, want %v", got, want)
	}
}

func TestParseRateLimitReset_Empty(t *testing.T) {
	if _, ok := parseRateLimitReset(""); ok {
		t.Error("parseRateLimitReset(\"\") ok = true, want false")
	}
}

func TestParseRateLimitReset_Garbage(t *testing.T) {
	if _, ok := parseRateLimitReset("not-a-valid-value"); ok {
		t.Error("parseRateLimitReset() ok = true, want false for an unparseable value")
	}
}

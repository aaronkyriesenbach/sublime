package provider_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/provider"
)

func TestQuotaExhaustedError_Error(t *testing.T) {
	resumeAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	err := &provider.QuotaExhaustedError{ResumeAt: resumeAt}
	want := "provider: quota exhausted until 2026-01-02T00:00:00Z"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestQuotaExhaustedError_Error_IncludesCause(t *testing.T) {
	resumeAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	cause := errors.New("daily quota exceeded")
	err := &provider.QuotaExhaustedError{ResumeAt: resumeAt, Cause: cause}
	if got := err.Error(); got != "provider: quota exhausted until 2026-01-02T00:00:00Z: daily quota exceeded" {
		t.Errorf("Error() = %q", got)
	}
}

func TestQuotaExhaustedError_Unwrap(t *testing.T) {
	cause := errors.New("underlying")
	err := &provider.QuotaExhaustedError{Cause: cause}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want true")
	}
}

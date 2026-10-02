package dispatcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
)

// dispatchWhisperThenOnline runs one English pair through [whisper, online]
// and returns the pair's final state plus how often the online Provider was
// searched.
func dispatchWhisperThenOnline(t *testing.T, whisperProvider provider.Provider, sharedTier bool) (domain.FileLanguageState, int32) {
	t.Helper()
	var onlineSearches atomic.Int32
	online := chainProvider(true, false)
	search := online.SearchFunc
	online.SearchFunc = func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
		onlineSearches.Add(1)
		return search(ctx, q)
	}

	d := dispatchWhisperChain(t, whisperProvider, online, sharedTier)
	return d.state, onlineSearches.Load()
}

func TestDispatcher_WhisperUnavailableSharedTierMateSubstitutes(t *testing.T) {
	state, _ := dispatchWhisperThenOnline(t, newWhisperProvider(t, deadSidecarURL(t), "eng"), true)

	if state.Status != domain.StatusSynced {
		t.Fatalf("state = %v/%v, want Synced via the Tier-mate while whisper is Unavailable", state.Status, state.FailureReason)
	}
}

func TestDispatcher_WhisperUnavailableLowerTierDoesNotTakeOver(t *testing.T) {
	state, onlineSearches := dispatchWhisperThenOnline(t, newWhisperProvider(t, deadSidecarURL(t), "eng"), false)

	if state.Status != domain.StatusPending || state.FailureReason != domain.FailureNone {
		t.Fatalf("state = %v/%v, want Pending (waiting for whisper), not Failed", state.Status, state.FailureReason)
	}
	if onlineSearches != 0 {
		t.Errorf("lower-Tier Provider searched %d times, want 0 while whisper is merely Unavailable", onlineSearches)
	}
}

func TestDispatcher_WhisperSidecar5xxFailsPairAsRetrievalFailed(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"failed to process audio"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(sidecar.Close)

	state, onlineSearches := dispatchWhisperThenOnline(t, newWhisperProvider(t, sidecar.URL, "eng"), false)

	if state.Status != domain.StatusFailed || state.FailureReason != domain.FailureRetrievalFailed {
		t.Fatalf("state = %v/%v, want Failed/retrieval_failed", state.Status, state.FailureReason)
	}
	if onlineSearches != 0 {
		t.Errorf("lower-Tier Provider searched %d times, want 0 after a retrieval failure", onlineSearches)
	}
}

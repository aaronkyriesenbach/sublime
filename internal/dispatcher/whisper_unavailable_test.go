package dispatcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/dispatcher"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/whisper"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

// instantClock is a retry.Clock that never waits.
type instantClock struct{}

func (instantClock) Sleep(context.Context, time.Duration) error { return nil }

// newWhisperProvider builds a whisper Provider whose single audio stream is
// English, pointed at endpoint.
func newWhisperProvider(t *testing.T, endpoint string) *whisper.Provider {
	t.Helper()
	p, err := whisper.New(whisper.Config{
		Endpoint: endpoint,
		Audio:    &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}},
		Clock:    instantClock{},
	})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}
	return p
}

// deadSidecarURL returns the address of a whisper sidecar that has been
// stopped, so connecting to it is refused.
func deadSidecarURL(t *testing.T) string {
	t.Helper()
	sidecar := httptest.NewServer(http.NotFoundHandler())
	url := sidecar.URL
	sidecar.Close()
	return url
}

// dispatchWhisperThenOnline runs one English pair through [whisper, online]
// — in one shared Tier or in two separate Tiers — for several dispatch
// passes and returns the pair's final state plus how often the online
// Provider was searched.
func dispatchWhisperThenOnline(t *testing.T, whisperProvider provider.Provider, sharedTier bool) (domain.FileLanguageState, int32) {
	t.Helper()
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, whisperChainVideoName)
	writeVideoFixture(t, videoPath)
	st := openTestStore(t)

	var onlineSearches atomic.Int32
	online := chainProvider(true, false)
	search := online.SearchFunc
	online.SearchFunc = func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
		onlineSearches.Add(1)
		return search(ctx, q)
	}

	whisperEntry := dispatcher.ProviderEntry{
		Name:        "whisper",
		Pipeline:    &pipeline.Pipeline{Store: st, Provider: whisperProvider, SyncEngine: &syncengine.FakeSyncEngine{}, Stripper: &pipeline.FakeStripper{}},
		WorkerCount: 1,
	}
	onlineEntry := dispatcher.ProviderEntry{
		Name:        "online",
		Pipeline:    &pipeline.Pipeline{Store: st, Provider: online, SyncEngine: &syncengine.FakeSyncEngine{}, Stripper: &pipeline.FakeStripper{}},
		WorkerCount: 1,
	}
	tiers := []dispatcher.ProviderTier{
		{Providers: []dispatcher.ProviderEntry{whisperEntry}},
		{Providers: []dispatcher.ProviderEntry{onlineEntry}},
	}
	if sharedTier {
		tiers = []dispatcher.ProviderTier{{Providers: []dispatcher.ProviderEntry{whisperEntry, onlineEntry}}}
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	ctx := context.Background()
	if _, err := whisperEntry.Pipeline.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	d := &dispatcher.Dispatcher{ProviderTiers: tiers, Store: st, Libraries: []domain.Library{lib}}
	for range 4 {
		if err := d.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil || !found {
		t.Fatalf("GetFile found=%v err=%v", found, err)
	}
	return file.Languages[0], onlineSearches.Load()
}

func TestDispatcher_WhisperUnavailableSharedTierMateSubstitutes(t *testing.T) {
	state, _ := dispatchWhisperThenOnline(t, newWhisperProvider(t, deadSidecarURL(t)), true)

	if state.Status != domain.StatusSynced {
		t.Fatalf("state = %v/%v, want Synced via the Tier-mate while whisper is Unavailable", state.Status, state.FailureReason)
	}
}

func TestDispatcher_WhisperUnavailableLowerTierDoesNotTakeOver(t *testing.T) {
	state, onlineSearches := dispatchWhisperThenOnline(t, newWhisperProvider(t, deadSidecarURL(t)), false)

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

	state, onlineSearches := dispatchWhisperThenOnline(t, newWhisperProvider(t, sidecar.URL), false)

	if state.Status != domain.StatusFailed || state.FailureReason != domain.FailureRetrievalFailed {
		t.Fatalf("state = %v/%v, want Failed/retrieval_failed", state.Status, state.FailureReason)
	}
	if onlineSearches != 0 {
		t.Errorf("lower-Tier Provider searched %d times, want 0 after a retrieval failure", onlineSearches)
	}
}

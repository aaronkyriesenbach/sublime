package dispatcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

const whisperChainVideoName = "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"

// instantClock is a retry.Clock that never waits.
type instantClock struct{}

func (instantClock) Sleep(context.Context, time.Duration) error { return nil }

// newWhisperProvider builds a whisper Provider whose single audio stream is
// tagged audioTag, pointed at endpoint.
func newWhisperProvider(t *testing.T, endpoint, audioTag string) *whisper.Provider {
	t.Helper()
	p, err := whisper.New(whisper.Config{
		Endpoint:    endpoint,
		Audio:       &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: audioTag}}},
		ChunkLength: 10 * time.Minute,
		Clock:       instantClock{},
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

// whisperDispatch is the observable outcome of dispatching one pair through
// a chain that starts with a whisper Provider.
type whisperDispatch struct {
	state       domain.FileLanguageState
	videoPath   string
	whisperSync *syncengine.FakeSyncEngine
}

// dispatchWhisperChain runs one English pair through [whisper, next] — in
// one shared Tier or in two separate Tiers — for several dispatch passes.
// next may be nil for a whisper-only chain.
func dispatchWhisperChain(t *testing.T, whisperProvider, next provider.Provider, sharedTier bool) whisperDispatch {
	t.Helper()
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, whisperChainVideoName)
	writeVideoFixture(t, videoPath)
	st := openTestStore(t)

	whisperSync := &syncengine.FakeSyncEngine{}
	whisperEntry := dispatcher.ProviderEntry{
		Name:        "whisper",
		Pipeline:    &pipeline.Pipeline{Store: st, Provider: whisperProvider, SyncEngine: whisperSync, Stripper: &pipeline.FakeStripper{}},
		WorkerCount: 1,
	}
	tiers := []dispatcher.ProviderTier{{Providers: []dispatcher.ProviderEntry{whisperEntry}}}
	if next != nil {
		nextEntry := dispatcher.ProviderEntry{
			Name:        "online",
			Pipeline:    &pipeline.Pipeline{Store: st, Provider: next, SyncEngine: &syncengine.FakeSyncEngine{}, Stripper: &pipeline.FakeStripper{}},
			WorkerCount: 1,
		}
		if sharedTier {
			tiers = []dispatcher.ProviderTier{{Providers: []dispatcher.ProviderEntry{whisperEntry, nextEntry}}}
		} else {
			tiers = append(tiers, dispatcher.ProviderTier{Providers: []dispatcher.ProviderEntry{nextEntry}})
		}
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
	return whisperDispatch{state: file.Languages[0], videoPath: videoPath, whisperSync: whisperSync}
}

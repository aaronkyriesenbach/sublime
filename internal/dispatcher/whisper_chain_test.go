package dispatcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/dispatcher"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/whisper"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

const whisperChainVideoName = "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4"

// whisperChainResult is the observable outcome of dispatching one pair
// through a chain that starts with the real whisper Provider.
type whisperChainResult struct {
	state       domain.FileLanguageState
	videoPath   string
	sidecarPath string
	whisperSync *syncengine.FakeSyncEngine
}

// runWhisperChain dispatches one English pair through [whisper, next...]
// (each in its own Tier) against a fake whisper sidecar and a fake audio
// source whose single audio stream is tagged audioTag. next may be nil for
// a whisper-only chain.
func runWhisperChain(t *testing.T, audioTag string, next *provider.Fake) whisperChainResult {
	t.Helper()

	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"segments":[{"start":0.5,"end":1.9,"text":" Generated line."}]}`))
	}))
	t.Cleanup(sidecar.Close)

	whisperProvider, err := whisper.New(whisper.Config{
		Endpoint: sidecar.URL,
		Audio:    &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: audioTag}}},
	})
	if err != nil {
		t.Fatalf("whisper.New: %v", err)
	}

	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, whisperChainVideoName)
	writeVideoFixture(t, videoPath)
	st := openTestStore(t)

	whisperSync := &syncengine.FakeSyncEngine{}
	whisperPipeline := &pipeline.Pipeline{Store: st, Provider: whisperProvider, SyncEngine: whisperSync, Stripper: &pipeline.FakeStripper{}}
	tiers := []dispatcher.ProviderTier{{Providers: []dispatcher.ProviderEntry{{Name: "whisper", Pipeline: whisperPipeline, WorkerCount: 1}}}}
	if next != nil {
		nextPipeline := &pipeline.Pipeline{Store: st, Provider: next, SyncEngine: &syncengine.FakeSyncEngine{}, Stripper: &pipeline.FakeStripper{}}
		tiers = append(tiers, dispatcher.ProviderTier{Providers: []dispatcher.ProviderEntry{{Name: "online", Pipeline: nextPipeline, WorkerCount: 1}}})
	}

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	ctx := context.Background()
	if _, err := whisperPipeline.Run(ctx, lib); err != nil {
		t.Fatalf("run: %v", err)
	}
	d := &dispatcher.Dispatcher{ProviderTiers: tiers, Store: st, Libraries: []domain.Library{lib}}
	for range 3 {
		if err := d.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil || !found {
		t.Fatalf("GetFile found=%v err=%v", found, err)
	}
	return whisperChainResult{
		state:       file.Languages[0],
		videoPath:   videoPath,
		sidecarPath: strings.TrimSuffix(videoPath, ".mp4") + ".en.srt",
		whisperSync: whisperSync,
	}
}

func TestDispatcher_WhisperOnlyChainWritesGeneratedSubtitleWithoutSync(t *testing.T) {
	r := runWhisperChain(t, "eng", nil)

	if r.state.Status != domain.StatusSynced {
		t.Fatalf("status = %v (reason %v), want Synced", r.state.Status, r.state.FailureReason)
	}
	if len(r.whisperSync.Calls) != 0 {
		t.Errorf("Sync calls = %d, want 0 for a Generated Subtitle", len(r.whisperSync.Calls))
	}

	content, err := os.ReadFile(r.sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar subtitle: %v", err)
	}
	if !strings.Contains(string(content), "Generated line.") {
		t.Errorf("sidecar does not contain the sidecar's transcript:\n%s", content)
	}
	hash, err := media.ComputeContentHash(r.videoPath)
	if err != nil {
		t.Fatalf("computing video hash: %v", err)
	}
	codec, _ := marker.CodecFor(".srt")
	if m, presence := codec.Read(content); presence != marker.Present || m.ContentHash != hash {
		t.Errorf("sidecar marker = %+v (presence %v), want hash %q Present", m, presence, hash)
	}
}

func TestDispatcher_WhisperLanguageMismatchFallsThroughToNextProvider(t *testing.T) {
	r := runWhisperChain(t, "jpn", chainProvider(true, false))

	if r.state.Status != domain.StatusSynced {
		t.Fatalf("status = %v (reason %v), want Synced via the next Provider", r.state.Status, r.state.FailureReason)
	}
	content, err := os.ReadFile(r.sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar subtitle: %v", err)
	}
	if strings.Contains(string(content), "Generated line.") {
		t.Errorf("sidecar holds a whisper transcript despite the language mismatch:\n%s", content)
	}
}

func TestDispatcher_WhisperOnlyChainLanguageMismatchLandsOnNoCandidate(t *testing.T) {
	r := runWhisperChain(t, "jpn", nil)

	if r.state.Status != domain.StatusFailed || r.state.FailureReason != domain.FailureNoCandidate {
		t.Fatalf("state = %v/%v, want Failed/no_candidate", r.state.Status, r.state.FailureReason)
	}
}

package dispatcher_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
)

// whisperChainResult is a whisper dispatch plus the path of the sidecar
// subtitle it would have written.
type whisperChainResult struct {
	whisperDispatch
	sidecarPath string
}

// runWhisperChain dispatches one English pair through [whisper, next] (each
// in its own Tier) against a fake whisper sidecar and a fake audio source
// whose single audio stream is tagged audioTag. next may be nil for a
// whisper-only chain.
func runWhisperChain(t *testing.T, audioTag string, next *provider.Fake) whisperChainResult {
	t.Helper()

	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"segments":[{"start":0.5,"end":1.9,"text":" Generated line."}]}`))
	}))
	t.Cleanup(sidecar.Close)

	var nextProvider provider.Provider
	if next != nil {
		nextProvider = next
	}
	d := dispatchWhisperChain(t, newWhisperProvider(t, sidecar.URL, audioTag), nextProvider, false)
	return whisperChainResult{
		whisperDispatch: d,
		sidecarPath:     strings.TrimSuffix(d.videoPath, ".mp4") + ".en.srt",
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

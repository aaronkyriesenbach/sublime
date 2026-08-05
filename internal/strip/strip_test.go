package strip_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

func TestSwap_WritesNewSidecarAndRemovesForeignSidecar(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	en := mustLang(t, "en")

	video := muxSubtitles(t, dir, "eng")
	foreignSidecar := writeSidecarFixture(t, dir, "muxed.en.ass", "foreign")

	s := strip.NewFFStripper()
	result, err := s.Swap(context.Background(), video, en, domain.StripScopeAll, hash, ".srt", []byte("synced content"))
	if err != nil {
		t.Fatalf("Swap returned error: %v", err)
	}

	if filepath.Base(result.SidecarPath) != "muxed.en.srt" {
		t.Errorf("expected sidecar path 'muxed.en.srt', got %q", filepath.Base(result.SidecarPath))
	}
	got, err := os.ReadFile(result.SidecarPath)
	if err != nil {
		t.Fatalf("reading written sidecar: %v", err)
	}
	if string(got) != "synced content" {
		t.Errorf("expected sidecar content 'synced content', got %q", got)
	}

	if len(result.RemovedSidecars) != 1 || filepath.Base(result.RemovedSidecars[0]) != "muxed.en.ass" {
		t.Errorf("expected the foreign sidecar removed, got %v", result.RemovedSidecars)
	}
	if _, err := os.Stat(foreignSidecar); !os.IsNotExist(err) {
		t.Errorf("expected foreign sidecar to be deleted, stat error: %v", err)
	}

	// Swap no longer touches embedded subtitle streams; that's the caller's
	// responsibility via Stripper.StripEmbedded, called separately before
	// Swap.
	streams, err := s.ProbeSubtitleStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("re-probing swapped video: %v", err)
	}
	if len(streams) != 1 {
		t.Errorf("expected the embedded subtitle stream to survive Swap, got %d streams", len(streams))
	}
}

func TestSwap_LeavesTrustedSidecarsAlone(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	en := mustLang(t, "en")

	video := copyFixtureVideo(t, dir)
	trustedOther := writeSidecarFixture(t, dir, "plain.es.srt", srtWithMarker(t, hash))

	s := strip.NewFFStripper()
	_, err := s.Swap(context.Background(), video, en, domain.StripScopeAll, hash, ".srt", []byte("synced en content"))
	if err != nil {
		t.Fatalf("Swap returned error: %v", err)
	}

	if _, err := os.Stat(trustedOther); err != nil {
		t.Errorf("expected trusted 'es' sidecar to survive an 'all' scope Swap, stat error: %v", err)
	}
}

func TestSwap_NoExistingSubtitlesAtAll(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	en := mustLang(t, "en")

	video := copyFixtureVideo(t, dir)

	s := strip.NewFFStripper()
	result, err := s.Swap(context.Background(), video, en, domain.StripScopeAll, hash, ".srt", []byte("synced content"))
	if err != nil {
		t.Fatalf("Swap returned error: %v", err)
	}

	if len(result.RemovedSidecars) != 0 {
		t.Errorf("expected nothing removed, got %+v", result)
	}
	if _, err := os.Stat(result.SidecarPath); err != nil {
		t.Errorf("expected the new sidecar to exist, stat error: %v", err)
	}
}

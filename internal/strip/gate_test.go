package strip_test

import (
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

func TestNeedsFetch_NoSidecarAtAll(t *testing.T) {
	dir := t.TempDir()
	needs, err := strip.NeedsFetch(dir, "Movie", mustLang(t, "en"), media.ContentHash("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NeedsFetch returned error: %v", err)
	}
	if !needs {
		t.Error("expected NeedsFetch = true with no existing sidecar")
	}
}

func TestNeedsFetch_ValidMatchingMarkerSkipsFetch(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	writeSidecarFixture(t, dir, "Movie.en.srt", srtWithMarker(t, hash))

	needs, err := strip.NeedsFetch(dir, "Movie", mustLang(t, "en"), hash)
	if err != nil {
		t.Fatalf("NeedsFetch returned error: %v", err)
	}
	if needs {
		t.Error("expected NeedsFetch = false when a valid, matching Marker is present")
	}
}

func TestNeedsFetch_MarkerBoundToStaleHashStillNeedsFetch(t *testing.T) {
	dir := t.TempDir()
	writeSidecarFixture(t, dir, "Movie.en.srt", srtWithMarker(t, media.ContentHash("aaaaaaaaaaaaaaaa")))

	needs, err := strip.NeedsFetch(dir, "Movie", mustLang(t, "en"), media.ContentHash("bbbbbbbbbbbbbbbb"))
	if err != nil {
		t.Fatalf("NeedsFetch returned error: %v", err)
	}
	if !needs {
		t.Error("expected NeedsFetch = true when the existing Marker is bound to a stale Content Hash")
	}
}

func TestNeedsFetch_ForeignSidecarStillNeedsFetch(t *testing.T) {
	dir := t.TempDir()
	writeSidecarFixture(t, dir, "Movie.en.srt", "1\n00:00:00,000 --> 00:00:01,000\nForeign\n")

	needs, err := strip.NeedsFetch(dir, "Movie", mustLang(t, "en"), media.ContentHash("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NeedsFetch returned error: %v", err)
	}
	if !needs {
		t.Error("expected NeedsFetch = true for a foreign sidecar with no Marker")
	}
}

func TestNeedsFetch_MarkerForDifferentLanguageDoesNotSatisfyGate(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	writeSidecarFixture(t, dir, "Movie.es.srt", srtWithMarker(t, hash))

	needs, err := strip.NeedsFetch(dir, "Movie", mustLang(t, "en"), hash)
	if err != nil {
		t.Fatalf("NeedsFetch returned error: %v", err)
	}
	if !needs {
		t.Error("expected NeedsFetch = true when only a different language's Marker is present")
	}
}

func TestNeedsFetch_UntaggedSidecarDoesNotSatisfyGate(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	writeSidecarFixture(t, dir, "Movie.srt", srtWithMarker(t, hash))

	needs, err := strip.NeedsFetch(dir, "Movie", mustLang(t, "en"), hash)
	if err != nil {
		t.Fatalf("NeedsFetch returned error: %v", err)
	}
	if !needs {
		t.Error("expected NeedsFetch = true when the only Marker'd sidecar is untagged (ambiguous language)")
	}
}

func TestNeedsFetch_PropagatesReadErrors(t *testing.T) {
	_, err := strip.NeedsFetch(filepath.Join(t.TempDir(), "does-not-exist"), "Movie", mustLang(t, "en"), media.ContentHash("0123456789abcdef"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

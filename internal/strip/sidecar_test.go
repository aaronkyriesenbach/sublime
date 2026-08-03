package strip_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

func writeSidecarFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture %q: %v", name, err)
	}
	return path
}

// srtWithMarker returns minimal SRT content with a trailing Marker cue bound
// to hash, using the real marker package so these tests exercise the actual
// on-disk format Strip has to recognize.
func srtWithMarker(t *testing.T, hash media.ContentHash) string {
	t.Helper()
	codec, ok := marker.CodecFor(".srt")
	if !ok {
		t.Fatal("no .srt MarkerCodec registered")
	}
	base := "1\n00:00:00,000 --> 00:00:01,000\nHello\n"
	out, err := codec.Write([]byte(base), marker.Marker{ContentHash: hash})
	if err != nil {
		t.Fatalf("writing marker: %v", err)
	}
	return string(out)
}

func TestListSidecars_RecognizesConfiguredExtensionsOnly(t *testing.T) {
	dir := t.TempDir()
	writeSidecarFixture(t, dir, "Movie.srt", "sub")
	writeSidecarFixture(t, dir, "Movie.ass", "sub")
	writeSidecarFixture(t, dir, "Movie.ssa", "sub")
	writeSidecarFixture(t, dir, "Movie.vtt", "sub")
	writeSidecarFixture(t, dir, "Movie.sub", "sub")
	writeSidecarFixture(t, dir, "Movie.nfo", "not a subtitle")
	writeSidecarFixture(t, dir, "Movie.mkv", "not a subtitle")
	writeSidecarFixture(t, dir, "Other.srt", "different basename")

	sidecars, err := strip.ListSidecars(dir, "Movie")
	if err != nil {
		t.Fatalf("ListSidecars returned error: %v", err)
	}

	if len(sidecars) != 5 {
		names := make([]string, len(sidecars))
		for i, s := range sidecars {
			names[i] = filepath.Base(s.Path)
		}
		sort.Strings(names)
		t.Fatalf("expected 5 recognized sidecars, got %d: %v", len(sidecars), names)
	}
}

func TestListSidecars_DetectsLanguageTag(t *testing.T) {
	dir := t.TempDir()
	writeSidecarFixture(t, dir, "Movie.en.srt", "sub")
	writeSidecarFixture(t, dir, "Movie.pt-BR.srt", "sub")
	writeSidecarFixture(t, dir, "Movie.srt", "sub")

	sidecars, err := strip.ListSidecars(dir, "Movie")
	if err != nil {
		t.Fatalf("ListSidecars returned error: %v", err)
	}
	if len(sidecars) != 3 {
		t.Fatalf("expected 3 sidecars, got %d", len(sidecars))
	}

	byName := map[string]strip.Sidecar{}
	for _, s := range sidecars {
		byName[filepath.Base(s.Path)] = s
	}

	en := byName["Movie.en.srt"]
	if !en.HasLanguage || en.Language.String() != "en" {
		t.Errorf("expected Movie.en.srt to detect language 'en', got %+v", en)
	}

	pt := byName["Movie.pt-BR.srt"]
	if !pt.HasLanguage || pt.Language.String() != "pt-BR" {
		t.Errorf("expected Movie.pt-BR.srt to detect language 'pt-BR', got %+v", pt)
	}

	untagged := byName["Movie.srt"]
	if untagged.HasLanguage {
		t.Errorf("expected Movie.srt to have no detected language, got %+v", untagged)
	}
}

func TestListSidecars_UnparsableLanguageSegmentIsUntagged(t *testing.T) {
	dir := t.TempDir()
	// "notalang" isn't a valid BCP-47 tag, so the whole file is treated as
	// an untagged sidecar rather than erroring.
	writeSidecarFixture(t, dir, "Movie.notalang.srt", "sub")

	sidecars, err := strip.ListSidecars(dir, "Movie")
	if err != nil {
		t.Fatalf("ListSidecars returned error: %v", err)
	}
	if len(sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(sidecars))
	}
	if sidecars[0].HasLanguage {
		t.Errorf("expected an unparsable language segment to leave the sidecar untagged, got %+v", sidecars[0])
	}
}

func TestIsSidecarTrusted_ValidMatchingMarker(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	path := writeSidecarFixture(t, dir, "Movie.en.srt", srtWithMarker(t, hash))

	trusted, err := strip.IsSidecarTrusted(path, hash)
	if err != nil {
		t.Fatalf("IsSidecarTrusted returned error: %v", err)
	}
	if !trusted {
		t.Error("expected a sidecar with a matching Marker to be trusted")
	}
}

func TestIsSidecarTrusted_MarkerBoundToStaleHash(t *testing.T) {
	dir := t.TempDir()
	path := writeSidecarFixture(t, dir, "Movie.en.srt", srtWithMarker(t, media.ContentHash("aaaaaaaaaaaaaaaa")))

	trusted, err := strip.IsSidecarTrusted(path, media.ContentHash("bbbbbbbbbbbbbbbb"))
	if err != nil {
		t.Fatalf("IsSidecarTrusted returned error: %v", err)
	}
	if trusted {
		t.Error("expected a sidecar bound to a stale Content Hash to be untrusted")
	}
}

func TestIsSidecarTrusted_NoMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	path := writeSidecarFixture(t, dir, "Movie.en.srt", "1\n00:00:00,000 --> 00:00:01,000\nForeign subtitle\n")

	trusted, err := strip.IsSidecarTrusted(path, media.ContentHash("0123456789abcdef"))
	if err != nil {
		t.Fatalf("IsSidecarTrusted returned error: %v", err)
	}
	if trusted {
		t.Error("expected a sidecar with no Marker to be untrusted")
	}
}

func TestIsSidecarTrusted_ExtensionWithNoRegisteredCodecIsAlwaysUntrusted(t *testing.T) {
	dir := t.TempDir()
	path := writeSidecarFixture(t, dir, "Movie.en.vtt", "WEBVTT\n")

	trusted, err := strip.IsSidecarTrusted(path, media.ContentHash("0123456789abcdef"))
	if err != nil {
		t.Fatalf("IsSidecarTrusted returned error: %v", err)
	}
	if trusted {
		t.Error("expected a sidecar with no registered MarkerCodec to be untrusted")
	}
}

func TestWriteSidecar_AtomicallyWritesToFinalPath(t *testing.T) {
	dir := t.TempDir()
	en := mustLang(t, "en")

	path, err := strip.WriteSidecar(dir, "Movie", en, ".srt", []byte("content"))
	if err != nil {
		t.Fatalf("WriteSidecar returned error: %v", err)
	}

	if filepath.Base(path) != "Movie.en.srt" {
		t.Errorf("expected final path 'Movie.en.srt', got %q", filepath.Base(path))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written sidecar: %v", err)
	}
	if string(got) != "content" {
		t.Errorf("expected written content 'content', got %q", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly 1 file after WriteSidecar (no leftover temp file), got %d", len(entries))
	}
}

func TestWriteSidecar_OverwritesExistingFileAtSamePath(t *testing.T) {
	dir := t.TempDir()
	en := mustLang(t, "en")
	writeSidecarFixture(t, dir, "Movie.en.srt", "old foreign content")

	path, err := strip.WriteSidecar(dir, "Movie", en, ".srt", []byte("new synced content"))
	if err != nil {
		t.Fatalf("WriteSidecar returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written sidecar: %v", err)
	}
	if string(got) != "new synced content" {
		t.Errorf("expected the new content to overwrite the old, got %q", got)
	}
}

func TestStripSidecars_AllScopeRemovesEveryUntrustedSidecarRegardlessOfLanguage(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	en := mustLang(t, "en")

	foreignEn := writeSidecarFixture(t, dir, "Movie.en.srt", "foreign")
	foreignPt := writeSidecarFixture(t, dir, "Movie.pt-BR.ass", "foreign")
	trustedOther := writeSidecarFixture(t, dir, "Movie.es.srt", srtWithMarker(t, hash))
	keep := writeSidecarFixture(t, dir, "Movie.en.new.srt", "just written")

	removed, err := strip.StripSidecars(dir, "Movie", domain.StripScopeAll, en, hash, keep)
	if err != nil {
		t.Fatalf("StripSidecars returned error: %v", err)
	}

	assertRemoved(t, removed, foreignEn, foreignPt)
	assertSurvives(t, trustedOther, keep)
}

func TestStripSidecars_PerLanguageScopeOnlyTouchesMatchingLanguage(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	en := mustLang(t, "en")

	foreignEn := writeSidecarFixture(t, dir, "Movie.en.ass", "foreign")
	foreignPt := writeSidecarFixture(t, dir, "Movie.pt-BR.srt", "foreign")
	untagged := writeSidecarFixture(t, dir, "Movie.srt", "foreign, no tag")

	removed, err := strip.StripSidecars(dir, "Movie", domain.StripScopePerLanguage, en, hash, "")
	if err != nil {
		t.Fatalf("StripSidecars returned error: %v", err)
	}

	assertRemoved(t, removed, foreignEn)
	assertSurvives(t, foreignPt, untagged)
}

func TestStripSidecars_LeavesTrustedMarkerAlone(t *testing.T) {
	dir := t.TempDir()
	hash := media.ContentHash("0123456789abcdef")
	en := mustLang(t, "en")

	trusted := writeSidecarFixture(t, dir, "Movie.en.srt", srtWithMarker(t, hash))

	removed, err := strip.StripSidecars(dir, "Movie", domain.StripScopeAll, en, hash, "")
	if err != nil {
		t.Fatalf("StripSidecars returned error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("expected no sidecars removed, got %v", removed)
	}
	assertSurvives(t, trusted)
}

func TestStripSidecars_StaleMarkerIsRemoved(t *testing.T) {
	dir := t.TempDir()
	en := mustLang(t, "en")
	stale := writeSidecarFixture(t, dir, "Movie.en.srt", srtWithMarker(t, media.ContentHash("aaaaaaaaaaaaaaaa")))

	removed, err := strip.StripSidecars(dir, "Movie", domain.StripScopeAll, en, media.ContentHash("bbbbbbbbbbbbbbbb"), "")
	if err != nil {
		t.Fatalf("StripSidecars returned error: %v", err)
	}

	assertRemoved(t, removed, stale)
}

func assertRemoved(t *testing.T, removed []string, want ...string) {
	t.Helper()
	if len(removed) != len(want) {
		t.Fatalf("expected %d removed sidecars, got %d: %v", len(want), len(removed), removed)
	}
	removedSet := map[string]bool{}
	for _, r := range removed {
		removedSet[r] = true
	}
	for _, w := range want {
		if !removedSet[w] {
			t.Errorf("expected %q to be removed, was not in %v", w, removed)
		}
		if _, err := os.Stat(w); !os.IsNotExist(err) {
			t.Errorf("expected %q to no longer exist on disk", w)
		}
	}
}

func assertSurvives(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q to survive, stat error: %v", p, err)
		}
	}
}

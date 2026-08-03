package strip_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

const fixtureVideo = "../../testdata/integration/video/sample.mp4"
const fixtureSubtitle = "../../testdata/integration/subs/sample.srt"

// requireBinary skips the test if name isn't on PATH, so this package's
// other (pure-logic) tests still run in environments without ffmpeg/ffprobe
// installed.
func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
}

// muxSubtitles produces a copy of fixtureVideo in dir with the given
// ISO-639-2-tagged subtitle tracks embedded, using real ffmpeg. Returns the
// muxed video's path.
func muxSubtitles(t *testing.T, dir string, languages ...string) string {
	t.Helper()
	requireBinary(t, "ffmpeg")

	out := filepath.Join(dir, "muxed.mp4")
	args := []string{"-y", "-v", "error", "-i", fixtureVideo}
	for range languages {
		args = append(args, "-i", fixtureSubtitle)
	}

	args = append(args, "-map", "0")
	for i := range languages {
		args = append(args, "-map", strconv.Itoa(i+1))
	}
	args = append(args, "-c", "copy", "-c:s", "mov_text")
	for i, lang := range languages {
		args = append(args, "-metadata:s:s:"+strconv.Itoa(i), "language="+lang)
	}
	args = append(args, out)

	cmd := exec.CommandContext(context.Background(), "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("muxing fixture video: %v\n%s", err, output)
	}
	return out
}

func copyFixtureVideo(t *testing.T, dir string) string {
	t.Helper()
	dst := filepath.Join(dir, "plain.mp4")
	data, err := os.ReadFile(fixtureVideo)
	if err != nil {
		t.Fatalf("reading fixture video: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("writing fixture video copy: %v", err)
	}
	return dst
}

func TestProbeSubtitleStreams_NoSubtitleStreams(t *testing.T) {
	requireBinary(t, "ffprobe")
	dir := t.TempDir()
	video := copyFixtureVideo(t, dir)

	s := strip.NewFFStripper()
	streams, err := s.ProbeSubtitleStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("ProbeSubtitleStreams returned error: %v", err)
	}
	if len(streams) != 0 {
		t.Errorf("expected 0 subtitle streams in the plain fixture, got %d: %+v", len(streams), streams)
	}
}

func TestProbeSubtitleStreams_FindsEmbeddedStreamsWithLanguageTags(t *testing.T) {
	dir := t.TempDir()
	video := muxSubtitles(t, dir, "eng", "spa")

	s := strip.NewFFStripper()
	streams, err := s.ProbeSubtitleStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("ProbeSubtitleStreams returned error: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("expected 2 subtitle streams, got %d: %+v", len(streams), streams)
	}

	languages := map[string]bool{}
	for _, s := range streams {
		languages[s.Language] = true
	}
	if !languages["eng"] || !languages["spa"] {
		t.Errorf("expected eng and spa language tags, got %+v", streams)
	}
}

func TestStripEmbedded_NoSubtitleStreamsLeavesVideoUntouched(t *testing.T) {
	requireBinary(t, "ffprobe")
	dir := t.TempDir()
	video := copyFixtureVideo(t, dir)

	before, err := os.Stat(video)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	s := strip.NewFFStripper()
	removed, err := s.StripEmbedded(context.Background(), video, domain.StripScopeAll, mustLang(t, "en"))
	if err != nil {
		t.Fatalf("StripEmbedded returned error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("expected no streams removed, got %v", removed)
	}

	after, err := os.Stat(video)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if before.ModTime() != after.ModTime() || before.Size() != after.Size() {
		t.Error("expected the video file to be untouched when there are no subtitle streams")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected no leftover temp files, found %d directory entries", len(entries))
	}
}

func TestStripEmbedded_AllScopeRemovesEverySubtitleStream(t *testing.T) {
	dir := t.TempDir()
	video := muxSubtitles(t, dir, "eng", "spa")

	s := strip.NewFFStripper()
	removed, err := s.StripEmbedded(context.Background(), video, domain.StripScopeAll, mustLang(t, "en"))
	if err != nil {
		t.Fatalf("StripEmbedded returned error: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("expected 2 streams removed, got %d: %v", len(removed), removed)
	}

	streams, err := s.ProbeSubtitleStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("re-probing stripped video: %v", err)
	}
	if len(streams) != 0 {
		t.Errorf("expected 0 subtitle streams after an 'all' scope strip, got %d: %+v", len(streams), streams)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected no leftover temp files, found %d directory entries", len(entries))
	}
}

func TestStripEmbedded_PerLanguageScopeOnlyRemovesMatchingStream(t *testing.T) {
	dir := t.TempDir()
	video := muxSubtitles(t, dir, "eng", "spa")

	s := strip.NewFFStripper()
	removed, err := s.StripEmbedded(context.Background(), video, domain.StripScopePerLanguage, mustLang(t, "en"))
	if err != nil {
		t.Fatalf("StripEmbedded returned error: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("expected 1 stream removed, got %d: %v", len(removed), removed)
	}

	streams, err := s.ProbeSubtitleStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("re-probing stripped video: %v", err)
	}
	if len(streams) != 1 || streams[0].Language != "spa" {
		t.Errorf("expected only the 'spa' stream to survive, got %+v", streams)
	}
}

func TestStripEmbedded_PerLanguageScopeLeavesUnresolvableTagsAlone(t *testing.T) {
	dir := t.TempDir()
	// "und" (undefined) has no crosswalk entry, so it must be left alone
	// under per_language scope even though it's foreign.
	video := muxSubtitles(t, dir, "und")

	s := strip.NewFFStripper()
	removed, err := s.StripEmbedded(context.Background(), video, domain.StripScopePerLanguage, mustLang(t, "en"))
	if err != nil {
		t.Fatalf("StripEmbedded returned error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("expected no streams removed for an unresolvable language tag, got %v", removed)
	}
}

func TestStripEmbedded_PreservesAudioAndVideoStreams(t *testing.T) {
	dir := t.TempDir()
	video := muxSubtitles(t, dir, "eng")

	s := strip.NewFFStripper()
	if _, err := s.StripEmbedded(context.Background(), video, domain.StripScopeAll, mustLang(t, "en")); err != nil {
		t.Fatalf("StripEmbedded returned error: %v", err)
	}

	cmd := exec.CommandContext(context.Background(), "ffprobe", "-v", "error",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", video)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe on stripped video: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if !slices.Contains(lines, "video") || !slices.Contains(lines, "audio") {
		t.Errorf("expected video and audio streams to survive, ffprobe output: %q", output)
	}
}

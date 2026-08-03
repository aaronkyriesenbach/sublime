// Package strip implements Sublime's Strip mechanism (see CONTEXT.md):
// removing a video's existing subtitles — both embedded streams and
// sidecar files — only once a verified, Synced replacement is ready
// (fetch-then-swap), while recognizing and leaving alone subtitles Sublime
// already produced for this exact file (per its Marker).
package strip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// SubtitleStream describes one embedded subtitle stream found by ffprobe.
type SubtitleStream struct {
	// Index is the stream's absolute index within the container, as
	// ffprobe reports it — the same number ffmpeg's `-map` stream
	// specifiers use.
	Index int

	// Language is the stream's raw ISO 639-2 language tag (e.g. "eng"), or
	// empty if the container carries no language tag for this stream.
	Language string
}

// FFStripper strips embedded subtitle streams using the real ffprobe/ffmpeg
// binaries. The zero value is not usable; construct with NewFFStripper.
type FFStripper struct {
	ffprobePath string
	ffmpegPath  string
}

// NewFFStripper returns an FFStripper that invokes "ffprobe" and "ffmpeg" as
// found on PATH.
func NewFFStripper() *FFStripper {
	return &FFStripper{ffprobePath: "ffprobe", ffmpegPath: "ffmpeg"}
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	Index int               `json:"index"`
	Tags  map[string]string `json:"tags"`
}

// ProbeSubtitleStreams runs ffprobe against the video at path and returns
// every subtitle stream it finds, in container order.
func (s *FFStripper) ProbeSubtitleStreams(ctx context.Context, path string) ([]SubtitleStream, error) {
	cmd := exec.CommandContext(ctx, s.ffprobePath,
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index:stream_tags=language",
		"-of", "json",
		path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("strip: ffprobe on %q: %w: %s", path, err, stderr.String())
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("strip: parsing ffprobe output for %q: %w", path, err)
	}

	streams := make([]SubtitleStream, len(parsed.Streams))
	for i, st := range parsed.Streams {
		streams[i] = SubtitleStream{Index: st.Index, Language: st.Tags["language"]}
	}
	return streams, nil
}

// StripEmbedded removes videoPath's embedded subtitle streams matching
// scope: every stream under domain.StripScopeAll, or only those whose
// language crosswalks to lang under domain.StripScopePerLanguage (a stream
// with an absent or unresolvable language tag is left untouched — see
// MatchesISO6392).
//
// ffprobe enumerates streams first; if nothing needs removing, the video
// file is never touched (no remux, no temp file). Otherwise ffmpeg
// stream-copies (no re-encode) into a sibling temp file, which is then
// renamed over the original — atomic within the same directory/filesystem.
// Returns the absolute container indices of the streams removed.
func (s *FFStripper) StripEmbedded(ctx context.Context, videoPath string, scope domain.StripScope, lang language.Tag) ([]int, error) {
	streams, err := s.ProbeSubtitleStreams(ctx, videoPath)
	if err != nil {
		return nil, err
	}

	var drop []int
	for _, st := range streams {
		if scope == domain.StripScopePerLanguage && !MatchesISO6392(st.Language, lang) {
			continue
		}
		drop = append(drop, st.Index)
	}
	if len(drop) == 0 {
		return nil, nil
	}

	if err := s.remuxDropping(ctx, videoPath, drop); err != nil {
		return nil, err
	}
	return drop, nil
}

// remuxDropping stream-copies videoPath into a sibling temp file excluding
// the given absolute stream indices, then renames it over the original.
func (s *FFStripper) remuxDropping(ctx context.Context, videoPath string, dropIndices []int) error {
	dir := filepath.Dir(videoPath)
	ext := filepath.Ext(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), ext)
	tmp := NewTempVideoPath(dir, stem, ext)

	args := []string{"-y", "-v", "error", "-i", videoPath, "-map", "0"}
	for _, idx := range dropIndices {
		args = append(args, "-map", "-0:"+strconv.Itoa(idx))
	}
	args = append(args, "-c", "copy", tmp)

	cmd := exec.CommandContext(ctx, s.ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("strip: ffmpeg remuxing %q: %w: %s", videoPath, err, stderr.String())
	}

	if err := renameOver(tmp, videoPath); err != nil {
		return err
	}
	return nil
}

package audiosource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// Silence detection thresholds. -30 dB sits above typical encoder noise
// floors yet below speech and music, and half a second is long enough to be
// a pause between phrases rather than a gap between words.
const (
	silenceNoiseFloor  = "-30dB"
	silenceMinDuration = "0.5"
)

var (
	silenceStartPattern = regexp.MustCompile(`silence_start: (-?\d+(?:\.\d+)?)`)
	silenceEndPattern   = regexp.MustCompile(`silence_end: (-?\d+(?:\.\d+)?)`)
)

// FFSource is a Source backed by the real ffprobe and ffmpeg binaries. The
// zero value is not usable; construct with NewFFSource.
type FFSource struct {
	ffprobePath string
	ffmpegPath  string
}

// NewFFSource returns an FFSource that invokes "ffprobe" and "ffmpeg" as
// found on PATH.
func NewFFSource() *FFSource {
	return &FFSource{ffprobePath: "ffprobe", ffmpegPath: "ffmpeg"}
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	Index int               `json:"index"`
	Tags  map[string]string `json:"tags"`
}

// AudioStreams implements Source.
func (s *FFSource) AudioStreams(ctx context.Context, videoPath string) ([]Stream, error) {
	cmd := exec.CommandContext(ctx, s.ffprobePath,
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index:stream_tags=language",
		"-of", "json",
		videoPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("audiosource: ffprobe on %q: %w: %s", videoPath, err, stderr.String())
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("audiosource: parsing ffprobe output for %q: %w", videoPath, err)
	}

	streams := make([]Stream, len(parsed.Streams))
	for i, st := range parsed.Streams {
		streams[i] = Stream{Index: st.Index, Language: normalizeLanguageTag(st.Tags["language"])}
	}
	return streams, nil
}

// Duration implements Source.
func (s *FFSource) Duration(ctx context.Context, videoPath string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, s.ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("audiosource: ffprobe duration of %q: %w: %s", videoPath, err, stderr.String())
	}

	secs, err := strconv.ParseFloat(string(bytes.TrimSpace(stdout.Bytes())), 64)
	if err != nil {
		return 0, fmt.Errorf("audiosource: parsing duration of %q: %w", videoPath, err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// Silences implements Source.
func (s *FFSource) Silences(ctx context.Context, videoPath string, streamIndex int) ([]Silence, error) {
	streams, err := s.AudioStreams(ctx, videoPath)
	if err != nil {
		return nil, err
	}
	if !hasStream(streams, streamIndex) {
		return nil, fmt.Errorf("%w: %q has no audio stream at index %d", ErrNoAudioStream, videoPath, streamIndex)
	}

	// silencedetect reports through the log, which -v error would hide.
	cmd := exec.CommandContext(ctx, s.ffmpegPath,
		"-v", "info", "-nostats", "-nostdin",
		"-i", videoPath,
		"-map", "0:"+strconv.Itoa(streamIndex),
		"-vn", "-sn", "-dn",
		"-af", "silencedetect=noise="+silenceNoiseFloor+":d="+silenceMinDuration,
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("audiosource: ffmpeg detecting silence in stream %d of %q: %w: %s", streamIndex, videoPath, err, stderr.String())
	}

	silences := parseSilences(stderr.Bytes())
	// A silence running to the end of the stream has a start but no end.
	if n := len(silences); n > 0 && silences[n-1].End < silences[n-1].Start {
		total, err := s.Duration(ctx, videoPath)
		if err != nil {
			return nil, err
		}
		silences[n-1].End = total
	}
	return silences, nil
}

// parseSilences extracts silencedetect's start/end log lines. A trailing
// silence without an end is returned with End before Start, for the caller
// to close at the stream's end.
func parseSilences(log []byte) []Silence {
	var silences []Silence
	for _, line := range bytes.Split(log, []byte("\n")) {
		if m := silenceStartPattern.FindSubmatch(line); m != nil {
			silences = append(silences, Silence{Start: parseSeconds(m[1]), End: -1})
		} else if m := silenceEndPattern.FindSubmatch(line); m != nil && len(silences) > 0 {
			silences[len(silences)-1].End = parseSeconds(m[1])
		}
	}
	return silences
}

func parseSeconds(text []byte) time.Duration {
	// The pattern only admits well-formed numbers.
	secs, _ := strconv.ParseFloat(string(text), 64)
	return time.Duration(secs * float64(time.Second))
}

// Extract implements Source.
func (s *FFSource) Extract(ctx context.Context, videoPath string, streamIndex int, r Range) ([]byte, error) {
	streams, err := s.AudioStreams(ctx, videoPath)
	if err != nil {
		return nil, err
	}
	if !hasStream(streams, streamIndex) {
		return nil, fmt.Errorf("%w: %q has no audio stream at index %d", ErrNoAudioStream, videoPath, streamIndex)
	}

	// -ss before -i seeks the demuxer instead of decoding and discarding
	// everything up to Start; ffmpeg still trims to the exact sample when
	// transcoding, so the range stays accurate.
	args := []string{"-v", "error", "-nostdin", "-ss", seconds(r.Start), "-i", videoPath}
	if r.Duration > 0 {
		args = append(args, "-t", seconds(r.Duration))
	}
	args = append(args,
		"-map", "0:"+strconv.Itoa(streamIndex),
		"-vn", "-sn", "-dn",
		"-ac", "1",
		"-ar", strconv.Itoa(SampleRate),
		"-f", "s16le",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, s.ffmpegPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("audiosource: ffmpeg extracting stream %d of %q: %w: %s", streamIndex, videoPath, err, stderr.String())
	}
	return encodeWAV(stdout.Bytes()), nil
}

func hasStream(streams []Stream, index int) bool {
	for _, st := range streams {
		if st.Index == index {
			return true
		}
	}
	return false
}

func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

package audiosource

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/langcode"
)

// SampleRate is the sample rate, in Hz, of every extracted audio range.
// whisper.cpp resamples to 16 kHz mono internally, so extracting it that
// way up front keeps requests small.
const SampleRate = 16000

// ErrNoAudioStream is returned by Source.Extract when the video has no
// audio stream with the requested index (including a video with no audio
// at all).
var ErrNoAudioStream = errors.New("audiosource: video has no such audio stream")

// Stream describes one audio stream in a video container.
type Stream struct {
	// Index is the stream's absolute index within the container, as
	// ffprobe reports it — the same number ffmpeg's `-map 0:<index>`
	// specifier uses.
	Index int

	// Language is the stream's lowercased ISO 639-2 language tag (e.g.
	// "eng"). It is empty for an untagged stream: the container carries no
	// tag, or tags it "und" (undefined). Muxers write "und" when no
	// language was set, so it carries no more information than absence.
	Language string
}

// Untagged reports whether the stream carries no usable language tag.
func (s Stream) Untagged() bool {
	return s.Language == ""
}

// MatchesLanguage reports whether the stream's language tag refers to the
// same base language as target, so a pt-BR target matches audio tagged
// "por". An untagged or unrecognized tag never matches.
func (s Stream) MatchesLanguage(target language.Tag) bool {
	return langcode.MatchesISO6392(s.Language, target)
}

// normalizeLanguageTag lowercases a raw container language tag and maps
// "und" to the empty (untagged) value.
func normalizeLanguageTag(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if tag == "und" {
		return ""
	}
	return tag
}

// Range selects a span of a stream's timeline.
type Range struct {
	// Start is the offset from the beginning of the video.
	Start time.Duration

	// Duration is the span length. Zero means "through the end of the
	// stream", which is how a caller extracts the whole stream.
	Duration time.Duration
}

// Silence is a stretch of a stream's timeline with no audible sound.
type Silence struct {
	Start time.Duration
	End   time.Duration
}

// Mid is the point halfway through the silence, the safest place to cut.
func (s Silence) Mid() time.Duration {
	return s.Start + (s.End-s.Start)/2
}

// Length is how long the silence lasts.
func (s Silence) Length() time.Duration {
	return s.End - s.Start
}

// Source reads a video's audio streams. The real implementation shells out
// to ffprobe/ffmpeg; FakeSource substitutes for it in tests.
type Source interface {
	// AudioStreams lists the video's audio streams in container order. A
	// video without audio yields an empty slice, not an error.
	AudioStreams(ctx context.Context, videoPath string) ([]Stream, error)

	// Duration reports the video's total length.
	Duration(ctx context.Context, videoPath string) (time.Duration, error)

	// Silences lists, in timeline order, the silent stretches of the audio
	// stream at streamIndex. It decodes the whole stream but keeps nothing
	// on disk. It returns ErrNoAudioStream if the video has no stream at
	// streamIndex.
	Silences(ctx context.Context, videoPath string, streamIndex int) ([]Silence, error)

	// Extract returns the given range of the audio stream at streamIndex
	// as a WAV file: 16 kHz, mono, 16-bit PCM. The audio is decoded
	// straight from the video, so memory use is bounded by the range, not
	// the video. It returns ErrNoAudioStream if the video has no stream at
	// streamIndex.
	Extract(ctx context.Context, videoPath string, streamIndex int, r Range) ([]byte, error)
}

// Package whisper is Sublime's speech-recognition Provider: instead of
// retrieving someone else's subtitle, it transcribes the video's own audio
// through a whisper.cpp server sidecar into a Generated Subtitle (see
// CONTEXT.md and docs/adr/0014-whisper-generated-subtitles-as-a-provider.md).
//
// A Generated Subtitle is same-language only: Search offers a Candidate
// only when an audio stream is tagged with the target's base language or,
// for untagged audio, the sidecar detects that language. Download always
// pins that language on the request and never asks the sidecar to
// translate.
package whisper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// Config configures a Provider.
type Config struct {
	// Endpoint is the whisper.cpp server's base URL (e.g.
	// "http://whisper:8080"). Required.
	Endpoint string

	// Audio probes the video's audio streams and extracts audio for the
	// sidecar. Required; production wiring passes audiosource.NewFFSource().
	Audio audiosource.Source

	// ChunkLength is the target audio length of each request: a video is
	// transcribed as a series of chunks about this long, cut at silence.
	// Zero means 10 minutes; negative is rejected.
	ChunkLength time.Duration

	// HTTPClient overrides the client used for sidecar requests; defaults
	// to http.DefaultClient.
	HTTPClient *http.Client

	// Logger receives the Provider's miss-cause and Suspension logging;
	// defaults to slog.Default() if nil.
	Logger *slog.Logger

	// Clock awaits the backoff between retries of a failing sidecar
	// request; defaults to retry.RealClock{}. Tests inject a fake.
	Clock retry.Clock

	// Now overrides the time source for Suspension; defaults to time.Now.
	Now func() time.Time
}

// Provider is Sublime's whisper Provider.
type Provider struct {
	client   *client
	audio    audiosource.Source
	log      *slog.Logger
	executor *retry.Executor
	now      func() time.Time

	chunkLength time.Duration

	mu sync.Mutex
	// suspendedUntil is when an Unavailable Suspension ends; zero when the
	// Provider is not suspended.
	suspendedUntil time.Time
	// unavailable stays set from the outage until a request is answered
	// again, which is what clears the Suspension and is logged as recovery.
	unavailable bool
}

var _ provider.Provider = (*Provider)(nil)

// New constructs a Provider from cfg. It performs no network calls.
func New(cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("whisper: Config.Endpoint is required")
	}
	if cfg.Audio == nil {
		return nil, errors.New("whisper: Config.Audio is required")
	}
	if cfg.ChunkLength < 0 {
		return nil, fmt.Errorf("whisper: Config.ChunkLength cannot be negative, got %s", cfg.ChunkLength)
	}
	chunkLength := cfg.ChunkLength
	if chunkLength == 0 {
		chunkLength = defaultChunkLength
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("whisper: invalid endpoint %q: %w", cfg.Endpoint, err)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clock := cfg.Clock
	if clock == nil {
		clock = retry.RealClock{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{
		client:      &client{baseURL: strings.TrimRight(endpoint.String(), "/"), httpClient: httpClient},
		audio:       cfg.Audio,
		log:         cfg.Logger,
		executor:    retry.NewExecutor(clock),
		now:         now,
		chunkLength: chunkLength,
	}, nil
}

func (p *Provider) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// NeverSynced reports that a Generated Subtitle is never Synced: its timing
// already derives from the video's audio. It implements the optional
// capability internal/pipeline checks to skip the sync stage.
func (p *Provider) NeverSynced() bool {
	return true
}

// candidateID is the payload of a Candidate's opaque ID: everything
// Download needs to regenerate the subtitle, since it receives nothing but
// the Candidate.
type candidateID struct {
	VideoPath   string `json:"video_path"`
	StreamIndex int    `json:"stream_index"`
	// Language is the base language code sent to the sidecar (e.g. "pt"
	// for a pt-BR target).
	Language string `json:"language"`
}

// detectionClipLength is the span of audio the sidecar listens to when
// detecting the language of an untagged stream: long enough for a reliable
// guess, short enough to stay cheap.
const detectionClipLength = 30 * time.Second

// Search decides eligibility from the video's audio. The first stream
// tagged with query.Language's base language yields exactly one hash-match
// Candidate, so scoring selects it unconditionally. Failing that, the first
// untagged stream is sampled and the sidecar asked to detect its language;
// the pair is eligible only if the detection equals the target. Otherwise
// it is an ordinary miss, with the specific cause logged.
func (p *Provider) Search(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
	if err := p.suspendedError(); err != nil {
		return nil, err
	}
	streams, err := p.audio.AudioStreams(ctx, query.Path)
	if err != nil {
		return nil, fmt.Errorf("whisper: probing audio streams: %w", err)
	}

	base, _ := query.Language.Base()
	target := base.String()

	for _, stream := range streams {
		if stream.MatchesLanguage(query.Language) {
			return p.candidateFor(query, stream, target)
		}
	}

	// A tag that names another language is a definite answer; only a stream
	// with no usable tag leaves the audio language unknown.
	for _, stream := range streams {
		if !stream.Untagged() {
			continue
		}
		detected, err := p.detectLanguage(ctx, query.Path, stream)
		if err != nil {
			return nil, err
		}
		if detectedMatches(detected, base) {
			return p.candidateFor(query, stream, target)
		}
		p.logger().Info("whisper: no candidate",
			"cause", fmt.Sprintf("detected language is %s, target is %s", detected, target),
			"path", query.Path, "language", query.Language.String())
		return nil, nil
	}

	p.logger().Info("whisper: no candidate",
		"cause", missCause(streams, target), "path", query.Path, "language", query.Language.String())
	return nil, nil
}

func (p *Provider) candidateFor(query provider.Query, stream audiosource.Stream, target string) ([]domain.Candidate, error) {
	id, err := json.Marshal(candidateID{VideoPath: query.Path, StreamIndex: stream.Index, Language: target})
	if err != nil {
		return nil, fmt.Errorf("whisper: encoding candidate ID: %w", err)
	}
	return []domain.Candidate{{
		ID:        string(id),
		Title:     query.Title,
		Year:      query.Year,
		Season:    query.Season,
		Episode:   query.Episode,
		HashMatch: true,
	}}, nil
}

// detectLanguage samples the middle of the video — its first seconds are
// often logos, intros or music — and asks the sidecar which language is
// spoken there.
func (p *Provider) detectLanguage(ctx context.Context, videoPath string, stream audiosource.Stream) (string, error) {
	duration, err := p.audio.Duration(ctx, videoPath)
	if err != nil {
		return "", fmt.Errorf("whisper: measuring video duration: %w", err)
	}
	clip := audiosource.Range{Duration: min(duration, detectionClipLength)}
	if duration > detectionClipLength {
		clip.Start = duration/2 - detectionClipLength/2
	}

	audio, err := p.audio.Extract(ctx, videoPath, stream.Index, clip)
	if err != nil {
		return "", fmt.Errorf("whisper: extracting detection clip: %w", err)
	}
	detected, err := p.detectSidecarLanguage(ctx, audio)
	if err != nil {
		return "", fmt.Errorf("whisper: detecting language: %w", err)
	}
	return detected, nil
}

// detectedMatches reports whether the sidecar's detected language, given as
// a code or an English name, is the target's base language. A name that
// resolves to no known language simply fails to match.
func detectedMatches(detected string, target language.Base) bool {
	detected = strings.ToLower(detected)
	return detected == target.String() ||
		detected == strings.ToLower(display.English.Languages().Name(target))
}

// missCause describes why no tagged stream matched target, e.g. "audio is
// jpn, target is en". Untagged streams never reach it: they go to language
// detection instead.
func missCause(streams []audiosource.Stream, target string) string {
	if len(streams) == 0 {
		return "video has no audio streams"
	}
	tags := make([]string, len(streams))
	for i, s := range streams {
		tags[i] = s.Language
	}
	return fmt.Sprintf("audio is %s, target is %s", strings.Join(tags, "/"), target)
}

// Download generates the subtitle for candidate: it transcribes the audio
// stream chunk by chunk, each extracted straight from the video, and returns
// the assembled segments as SRT. The transcript is held in memory only, so
// an interrupted run starts over.
func (p *Provider) Download(ctx context.Context, candidate domain.Candidate) ([]byte, error) {
	var id candidateID
	if err := json.Unmarshal([]byte(candidate.ID), &id); err != nil || id.VideoPath == "" || id.Language == "" {
		return nil, fmt.Errorf("whisper: candidate ID %q is not a whisper candidate", candidate.ID)
	}

	if err := p.suspendedError(); err != nil {
		return nil, err
	}

	total, err := p.audio.Duration(ctx, id.VideoPath)
	if err != nil {
		return nil, fmt.Errorf("whisper: reading video duration: %w", err)
	}
	chunks, err := planChunks(total, p.chunkLength, func() ([]audiosource.Silence, error) {
		silences, err := p.audio.Silences(ctx, id.VideoPath, id.StreamIndex)
		if err != nil {
			return nil, fmt.Errorf("whisper: detecting silence: %w", err)
		}
		return silences, nil
	})
	if err != nil {
		return nil, err
	}

	var transcript []segment
	for i, c := range chunks {
		p.logger().Info(fmt.Sprintf("whisper: chunk %d of %d", i+1, len(chunks)),
			"path", id.VideoPath, "start", c.extract.Start)

		segments, err := p.transcribeChunk(ctx, id, c, total)
		if err != nil {
			return nil, fmt.Errorf("whisper: chunk %d of %d: %w", i+1, len(chunks), err)
		}

		segments = trim(segments, c.keepFrom, c.keepUntil)
		if c.keepFrom > -unbounded {
			segments = dropRepeatedSeam(transcript, segments)
		}
		transcript = append(transcript, segments...)
	}

	srt := formatSRT(shapeCues(transcript))
	if len(srt) == 0 {
		return nil, errors.New("whisper: transcript contains no speech")
	}
	return srt, nil
}

// transcribeChunk extracts one chunk's audio and returns its segments with
// timestamps on the whole video's timeline.
func (p *Provider) transcribeChunk(ctx context.Context, id candidateID, c chunk, total time.Duration) ([]segment, error) {
	audio, err := p.audio.Extract(ctx, id.VideoPath, id.StreamIndex, c.extract)
	if err != nil {
		return nil, fmt.Errorf("extracting audio: %w", err)
	}

	segments, err := p.transcribe(ctx, audio, id.Language, c.timeout(total))
	if err != nil {
		return nil, fmt.Errorf("transcribing: %w", err)
	}

	for i := range segments {
		segments[i] = segments[i].offset(c.extract.Start)
	}
	return segments, nil
}

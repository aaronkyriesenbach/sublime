// Package whisper is Sublime's speech-recognition Provider: instead of
// retrieving someone else's subtitle, it transcribes the video's own audio
// through a whisper.cpp server sidecar into a Generated Subtitle (see
// CONTEXT.md and docs/adr/0014-whisper-generated-subtitles-as-a-provider.md).
//
// A Generated Subtitle is same-language only: Search offers a Candidate
// only when an audio stream is tagged with the target's base language, and
// Download always pins that language on the request and never asks the
// sidecar to translate.
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
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
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

	// Logger receives the Provider's miss-cause logging; defaults to
	// slog.Default() if nil.
	Logger *slog.Logger
}

// Provider is Sublime's whisper Provider.
type Provider struct {
	client *client
	audio  audiosource.Source
	log    *slog.Logger

	chunkLength time.Duration
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
	return &Provider{
		client: &client{baseURL: strings.TrimRight(endpoint.String(), "/"), httpClient: httpClient},
		audio:  cfg.Audio,
		log:    cfg.Logger,

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

// Search decides eligibility from the video's audio stream tags alone: the
// first stream tagged with query.Language's base language yields exactly
// one hash-match Candidate, so scoring selects it unconditionally.
// Otherwise it is an ordinary miss, with the specific cause logged.
func (p *Provider) Search(ctx context.Context, query provider.Query) ([]domain.Candidate, error) {
	streams, err := p.audio.AudioStreams(ctx, query.Path)
	if err != nil {
		return nil, fmt.Errorf("whisper: probing audio streams: %w", err)
	}

	base, _ := query.Language.Base()
	target := base.String()

	for _, stream := range streams {
		if !stream.MatchesLanguage(query.Language) {
			continue
		}
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

	p.logger().Info("whisper: no candidate",
		"cause", missCause(streams, target), "path", query.Path, "language", query.Language.String())
	return nil, nil
}

// missCause describes why no stream matched target, e.g. "audio is ja,
// target is en".
func missCause(streams []audiosource.Stream, target string) string {
	if len(streams) == 0 {
		return "video has no audio streams"
	}
	tags := make([]string, len(streams))
	for i, s := range streams {
		tags[i] = s.Language
		if s.Untagged() {
			tags[i] = "untagged"
		}
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

	srt := formatSRT(transcript)
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

	ctx, cancel := context.WithTimeout(ctx, c.timeout(total))
	defer cancel()
	segments, err := p.client.transcribe(ctx, audio, id.Language)
	if err != nil {
		return nil, fmt.Errorf("transcribing: %w", err)
	}

	for i := range segments {
		segments[i] = segments[i].offset(c.extract.Start)
	}
	return segments, nil
}

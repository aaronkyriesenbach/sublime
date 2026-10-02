package audiosource

import (
	"context"
	"fmt"
	"sync"
)

// ExtractCall records a single Extract invocation on a FakeSource.
type ExtractCall struct {
	VideoPath   string
	StreamIndex int
	Range       Range
}

// FakeSource is a Source test double that never shells out to ffprobe or
// ffmpeg. Safe for concurrent use by multiple pipeline workers.
type FakeSource struct {
	mu sync.Mutex

	// Streams is returned by AudioStreams for every video path.
	Streams []Stream

	// StreamsErr, if set, is returned by AudioStreams instead of Streams.
	StreamsErr error

	// Audio, if non-nil, is returned verbatim by Extract. Left nil, Extract
	// returns silence of the requested Range.Duration as a valid WAV.
	Audio []byte

	// ExtractErr, if set, is returned by Extract instead of any audio.
	ExtractErr error

	// ExtractCalls records every Extract invocation, in order.
	ExtractCalls []ExtractCall
}

// AudioStreams implements Source.
func (f *FakeSource) AudioStreams(_ context.Context, _ string) ([]Stream, error) {
	if f.StreamsErr != nil {
		return nil, f.StreamsErr
	}
	return append([]Stream(nil), f.Streams...), nil
}

// Extract implements Source. Like the real implementation, it reports
// ErrNoAudioStream for an index that is not in Streams.
func (f *FakeSource) Extract(_ context.Context, videoPath string, streamIndex int, r Range) ([]byte, error) {
	f.mu.Lock()
	f.ExtractCalls = append(f.ExtractCalls, ExtractCall{VideoPath: videoPath, StreamIndex: streamIndex, Range: r})
	f.mu.Unlock()

	if f.ExtractErr != nil {
		return nil, f.ExtractErr
	}
	if !hasStream(f.Streams, streamIndex) {
		return nil, fmt.Errorf("%w: fake has no audio stream at index %d", ErrNoAudioStream, streamIndex)
	}
	if f.Audio != nil {
		return f.Audio, nil
	}

	samples := int(r.Duration.Seconds() * SampleRate)
	return encodeWAV(make([]byte, samples*2)), nil
}

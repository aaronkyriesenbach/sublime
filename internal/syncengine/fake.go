package syncengine

import (
	"fmt"
	"os"
	"sync"
)

// Call records a single Sync invocation on a FakeSyncEngine, so tests can
// assert what the rest of the pipeline passed to it.
type Call struct {
	ReferenceVideoPath    string
	CandidateSubtitlePath string
	OutputPath            string
}

// FakeSyncEngine is a SyncEngine test double that never shells out to a
// real alignment binary. By default it passes the candidate subtitle
// through unchanged; set Err or OutputContent per test to exercise
// failure or fixed-shift behavior instead. Safe for concurrent use by
// multiple pipeline workers.
type FakeSyncEngine struct {
	mu sync.Mutex

	// Err, if set, is returned by Sync instead of writing any output.
	Err error

	// OutputContent, if non-nil, is written to outputPath verbatim instead of
	// the candidate subtitle's own bytes, simulating a fixed timing shift
	// without needing real subtitle-format parsing. Leave nil for the default
	// passthrough behavior.
	OutputContent []byte

	// Calls records every Sync invocation, in order.
	Calls []Call
}

// Sync implements SyncEngine.
func (f *FakeSyncEngine) Sync(referenceVideoPath, candidateSubtitlePath, outputPath string) (string, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{
		ReferenceVideoPath:    referenceVideoPath,
		CandidateSubtitlePath: candidateSubtitlePath,
		OutputPath:            outputPath,
	})
	f.mu.Unlock()

	if f.Err != nil {
		return "", f.Err
	}

	content := f.OutputContent
	if content == nil {
		data, err := os.ReadFile(candidateSubtitlePath)
		if err != nil {
			return "", fmt.Errorf("reading candidate subtitle %q: %w", candidateSubtitlePath, err)
		}
		content = data
	}

	if err := os.WriteFile(outputPath, content, 0o644); err != nil {
		return "", fmt.Errorf("writing sync output %q: %w", outputPath, err)
	}

	return outputPath, nil
}

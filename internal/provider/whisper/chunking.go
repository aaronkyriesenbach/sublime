package whisper

import (
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
)

const (
	// defaultChunkLength applies when Config.ChunkLength is left unset.
	defaultChunkLength = 10 * time.Minute

	// cutWindowFraction sizes the window around the target length in which
	// a silence is accepted as a cut, as a fraction of the target. Wide
	// enough that a feature film nearly always offers a pause, narrow
	// enough that chunks stay close to the configured length.
	cutWindowFraction = 5

	// seamOverlap is how much audio two chunks share when there is no
	// silence to cut at. Long enough to hear a whole word twice, short
	// enough to cost little extra inference.
	seamOverlap = 2 * time.Second

	// seamSlack widens each side's share of the transcript past the seam,
	// so a word whose timestamp differs slightly between the two chunks'
	// transcriptions is heard by at least one of them. The words both keep
	// are then de-duplicated by text.
	seamSlack = seamOverlap / 4
)

// chunk is one request's span of the video's timeline.
type chunk struct {
	// extract is the audio span sent to the sidecar. A zero Duration runs
	// through the end of the stream.
	extract audiosource.Range

	// keepFrom and keepUntil bound the speech this chunk contributes to the
	// transcript. They are only bounded at a fixed cut, where neighbouring
	// chunks hear the same audio; a silence cut has nothing to trim.
	keepFrom, keepUntil time.Duration
}

const unbounded = time.Duration(1<<63 - 1)

// planChunks cuts a video of the given total length into chunks of about
// target. A cut lands at the silence nearest the target within a window
// around it; with no silence there, it is a fixed cut at the target with
// the neighbouring chunks overlapping. A video no longer than the target
// plus the window is a single chunk, which also never needs silences.
func planChunks(total, target time.Duration, silences func() ([]audiosource.Silence, error)) ([]chunk, error) {
	window := target / cutWindowFraction
	if total <= target+window {
		return []chunk{{keepFrom: -unbounded, keepUntil: unbounded}}, nil
	}

	found, err := silences()
	if err != nil {
		return nil, err
	}

	var chunks []chunk
	start := time.Duration(0)
	extractStart := time.Duration(0)
	keepFrom := -unbounded
	for total-start > target+window {
		c := chunk{keepFrom: keepFrom, keepUntil: unbounded}
		if cut, ok := silenceCut(found, start+target-window, start+target+window, start+target); ok {
			c.extract = audiosource.Range{Start: extractStart, Duration: cut - extractStart}
			start, extractStart, keepFrom = cut, cut, -unbounded
		} else {
			cut := start + target
			c.extract = audiosource.Range{Start: extractStart, Duration: cut + seamOverlap/2 - extractStart}
			c.keepUntil = cut + seamSlack
			start, extractStart, keepFrom = cut, cut-seamOverlap/2, cut-seamSlack
		}
		chunks = append(chunks, c)
	}
	return append(chunks, chunk{
		extract:  audiosource.Range{Start: extractStart},
		keepFrom: keepFrom, keepUntil: unbounded,
	}), nil
}

// silenceCut picks the middle of the longest silence whose middle falls in
// [from, to], preferring the one nearest target on a tie.
func silenceCut(silences []audiosource.Silence, from, to, target time.Duration) (time.Duration, bool) {
	var best audiosource.Silence
	found := false
	for _, s := range silences {
		mid := s.Mid()
		if mid < from || mid > to {
			continue
		}
		if !found || s.Length() > best.Length() ||
			(s.Length() == best.Length() && distance(mid, target) < distance(best.Mid(), target)) {
			best, found = s, true
		}
	}
	return best.Mid(), found
}

func distance(a, b time.Duration) time.Duration {
	if a < b {
		return b - a
	}
	return a - b
}

// timeout bounds one chunk's request. Inference can run at roughly real
// time on a CPU-only sidecar, so the allowance grows with the audio length,
// with a fixed margin for model start-up and upload.
func (c chunk) timeout(total time.Duration) time.Duration {
	length := c.extract.Duration
	if length == 0 {
		length = total - c.extract.Start
	}
	return 3*length + time.Minute
}

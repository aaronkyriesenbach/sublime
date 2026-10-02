package whisper

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	// maxChunkAttempts bounds how often one chunk is transcribed: the first
	// try plus two retries.
	maxChunkAttempts = 3

	// maxRepeatedRun is how many identical lines in a row make a chunk's
	// output a decoding loop.
	maxRepeatedRun = 10

	// minLinesForDominance keeps a short exchange of identical replies
	// ("No." "No." "Yes.") from reading as one repeated line dominating.
	minLinesForDominance = 10

	// longRuntime is the video length from which a transcript with almost
	// no speech is distrusted; shorter videos (clips, shorts) may
	// legitimately be nearly wordless.
	longRuntime = 30 * time.Minute

	// minWordsPerMinute is the least speech a long video's transcript must
	// hold. Even sparse dialogue clears it; a hallucinating or deaf
	// transcription does not.
	minWordsPerMinute = 1.0
)

// retryTemperatures are the decoding temperatures of a chunk's retries, in
// order. The first attempt leaves the sidecar's default, which repeats the
// same greedy decoding that a loop or an empty result came from; raising
// the temperature is what lets a retry escape it.
var retryTemperatures = [maxChunkAttempts - 1]float64{0.4, 0.8}

// temperatureFor is the temperature for the given attempt (0-based); zero
// means the sidecar's default.
func temperatureFor(attempt int) float64 {
	if attempt == 0 {
		return 0
	}
	return retryTemperatures[attempt-1]
}

var (
	errEmptyChunk     = errors.New("no speech in output")
	errRepeatedOutput = errors.New("output is dominated by a repeated line")
)

// chunkProblem reports why a chunk's segments cannot be trusted, or nil.
func chunkProblem(segments []segment) error {
	if len(shapeCues(segments)) == 0 {
		return errEmptyChunk
	}
	return repetitionProblem(segments)
}

// repetitionProblem reports a decoding loop: a long run of one line, or one
// line making up most of the output.
func repetitionProblem(segments []segment) error {
	var lines []string
	for _, s := range segments {
		if line := normalizedLine(s); line != "" {
			lines = append(lines, line)
		}
	}

	counts := map[string]int{}
	run := 0
	for i, line := range lines {
		counts[line]++
		if i > 0 && line == lines[i-1] {
			run++
		} else {
			run = 1
		}
		if run >= maxRepeatedRun {
			return fmt.Errorf("%w (%q repeated %d times in a row)", errRepeatedOutput, line, run)
		}
	}
	if len(lines) < minLinesForDominance {
		return nil
	}
	for line, n := range counts {
		if n*2 > len(lines) {
			return fmt.Errorf("%w (%q is %d of %d lines)", errRepeatedOutput, line, n, len(lines))
		}
	}
	return nil
}

// normalizedLine is a segment's text reduced to lowercase letters and
// digits, so a repeated line is recognised despite punctuation and spacing.
func normalizedLine(s segment) string {
	var b strings.Builder
	for _, u := range s.units() {
		for _, r := range u.text {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(unicode.ToLower(r))
			}
		}
		b.WriteByte(' ')
	}
	return strings.TrimSpace(b.String())
}

// transcriptProblem reports why an assembled transcript of a video of the
// given length cannot be shipped, or nil.
func transcriptProblem(segments []segment, cues []cue, total time.Duration) error {
	if len(cues) == 0 {
		return errors.New("transcript contains no speech")
	}
	if err := repetitionProblem(segments); err != nil {
		return err
	}
	if total < longRuntime {
		return nil
	}
	words := 0
	for _, c := range cues {
		words += len(strings.Fields(c.text()))
	}
	if float64(words) < total.Minutes()*minWordsPerMinute {
		return fmt.Errorf("transcript has only %d words for a %s video", words, total.Round(time.Second))
	}
	return nil
}

// Package alass implements syncengine.SyncEngine by shelling out to the
// real alass CLI (github.com/kaegi/alass) instead of a fake. See
// CONTEXT.md's "Sync Engine" entry and issue #14's alass CLI contract
// research for the invocation, format-dispatch, and exit-code details this
// package encodes.
package alass

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// defaultBinary is the executable assumed to be on PATH when
// Engine.BinaryPath is unset. alass's own release artifact ships as
// alass-linux64, not alass — Sublime's packaging is responsible for making
// an "alass" binary available under this name, not this package.
const defaultBinary = "alass"

// subtitleExtensions are the six subtitle formats alass recognizes by file
// extension (kaegi/alass's InputFileHandler::open) — wider than alass's own
// README, which documents only three. Anything else supplied as a
// reference is treated as a video and handed to ffmpeg/ffprobe for audio
// extraction instead.
var subtitleExtensions = map[string]bool{
	".srt": true,
	".ass": true,
	".ssa": true,
	".idx": true,
	".sub": true,
	".vob": true,
}

// referenceKind classifies how alass dispatches a reference input.
type referenceKind int

const (
	referenceKindVideo referenceKind = iota
	referenceKindSubtitle
)

// classifyReference reports how alass will dispatch path purely by file
// extension, mirroring alass's own extension table: one of its six
// recognized subtitle formats, or (anything else) a video.
func classifyReference(path string) referenceKind {
	if subtitleExtensions[strings.ToLower(filepath.Ext(path))] {
		return referenceKindSubtitle
	}
	return referenceKindVideo
}

// Engine invokes the real alass binary to perform Sync.
type Engine struct {
	// BinaryPath is the alass executable to invoke. Empty means "alass" on
	// PATH.
	BinaryPath string

	// Flags are extra CLI flags appended after the three positional
	// arguments (e.g. "--no-split"). Left nil, alass runs with its own
	// documented defaults.
	Flags []string
}

// New returns an Engine that invokes "alass" on PATH with no extra flags.
func New() *Engine {
	return &Engine{}
}

// RunError describes a non-zero alass exit. alass has exactly two exit
// codes (0 success, 1 any failure) with no per-category codes, so ExitCode
// is only ever 1 in practice but is recorded for completeness. alass prints
// its only error diagnostics to stdout, never stderr, as a chained
// "error: ... / caused by: ..." message; Output carries that text verbatim.
//
// A RunError is always a terminal failure: alass's alignment algorithm is
// deterministic, so a run that failed once fails identically on retry.
// Callers must not wrap it in retry.Transient.
type RunError struct {
	ExitCode int
	Output   string
}

func (e *RunError) Error() string {
	out := strings.TrimSpace(e.Output)
	if out == "" {
		return fmt.Sprintf("alass exited with status %d", e.ExitCode)
	}
	return fmt.Sprintf("alass exited with status %d: %s", e.ExitCode, out)
}

// Sync implements syncengine.SyncEngine by invoking:
//
//	alass <referenceVideoPath> <candidateSubtitlePath> <outputPath> [flags]
//
// referenceVideoPath is dispatched by alass itself purely by file
// extension: one of the six recognized subtitle formats, or (any other
// extension) a video it extracts audio from via ffmpeg/ffprobe.
// candidateSubtitlePath must itself be one of the six recognized subtitle
// extensions — it is always a subtitle, never a video — and outputPath must
// share that same extension, since alass never converts between subtitle
// formats and hard-errors on a mismatch before running. Both are validated
// here so a caller gets a precise error without invoking the subprocess.
//
// A successful (exit 0) run's output is trusted as-is: alass always
// produces a best-scoring delta and never signals alignment quality, so
// there is no plausibility check here — see issue #14 and issue #26.
func (e *Engine) Sync(referenceVideoPath, candidateSubtitlePath, outputPath string) (string, error) {
	if classifyReference(candidateSubtitlePath) != referenceKindSubtitle {
		return "", fmt.Errorf("candidate subtitle %q has an unsupported extension (must be one of .srt, .ass, .ssa, .idx, .sub, .vob)", candidateSubtitlePath)
	}
	candidateExt := strings.ToLower(filepath.Ext(candidateSubtitlePath))
	if outputExt := strings.ToLower(filepath.Ext(outputPath)); outputExt != candidateExt {
		return "", fmt.Errorf("output path %q must use the candidate subtitle's own extension %q (alass does not convert between subtitle formats)", outputPath, candidateExt)
	}

	bin := e.BinaryPath
	if bin == "" {
		bin = defaultBinary
	}

	args := append([]string{referenceVideoPath, candidateSubtitlePath, outputPath}, e.Flags...)
	cmd := exec.Command(bin, args...)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &RunError{ExitCode: exitErr.ExitCode(), Output: stdout.String()}
		}
		return "", fmt.Errorf("running alass: %w", err)
	}

	return outputPath, nil
}

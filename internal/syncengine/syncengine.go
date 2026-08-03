// Package syncengine defines the pluggable interface Sublime uses to
// perform Sync — actively re-timing a Candidate subtitle's timestamps to
// align with a video's actual audio (see the "Sync Engine" and "Sync"
// entries in CONTEXT.md at the repo root) — along with a fake
// implementation for testing the rest of the pipeline without invoking a
// real external binary such as alass.
package syncengine

// SyncEngine aligns a candidate subtitle's timestamps to a reference
// video's actual audio, writing the result to outputPath.
//
// The signature is deliberately alass-agnostic: referenceVideoPath and
// candidateSubtitlePath are read-only inputs, outputPath is where the
// aligned subtitle should be written in the candidate's own format, and
// implementations return outputPath on success or a non-nil error — never
// both. A later, alass-backed implementation shells out to the alass CLI
// against this same interface.
type SyncEngine interface {
	Sync(referenceVideoPath, candidateSubtitlePath, outputPath string) (string, error)
}

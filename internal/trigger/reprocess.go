package trigger

import (
	"context"
	"fmt"
	"os"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
)

// Reprocess registers target for a forced reprocess, bypassing the
// Marker+Content-Hash gate (pipeline.WithForce) once the Dispatcher later
// claims and processes it. target may be:
//
//   - a path to a single video file, reprocessed on its own
//   - a path to a directory, reprocessing every video file under it
//     (recursively)
//   - "" or lib.Path itself, reprocessing the entire Library
//
// It's a pure registration/reset operation: it returns as soon as target's
// (file, language) pairs have been reset to Pending, without waiting for
// any actual resync to happen — see
// docs/adr/0004-decouple-trigger-and-dispatcher.md.
func Reprocess(ctx context.Context, p *pipeline.Pipeline, lib domain.Library, target string) error {
	if target == "" {
		target = lib.Path
	}

	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("trigger: reprocess target %q: %w", target, err)
	}

	if info.IsDir() {
		scoped := lib
		scoped.Path = target
		_, err := p.Run(ctx, scoped, pipeline.WithForce())
		return err
	}

	if !pipeline.IsVideoFile(target) {
		return fmt.Errorf("trigger: reprocess target %q is not a recognized video file", target)
	}

	_, err = p.RunFile(ctx, lib, target, pipeline.WithForce())
	return err
}

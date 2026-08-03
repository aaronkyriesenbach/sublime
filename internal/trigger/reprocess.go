package trigger

import (
	"context"
	"fmt"
	"os"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
)

// Reprocess forces target through the pipeline regardless of prior state,
// bypassing the Marker+Content-Hash gate (pipeline.WithForce). target may
// be:
//
//   - a path to a single video file, reprocessed on its own
//   - a path to a directory, reprocessing every video file under it
//     (recursively)
//   - "" or lib.Path itself, reprocessing the entire Library
//
// It returns once processing completes; there is no periodic rescanning
// implied by this call, only the one forced pass.
func Reprocess(ctx context.Context, p *pipeline.Pipeline, lib domain.Library, target string) (pipeline.Result, error) {
	if target == "" {
		target = lib.Path
	}

	info, err := os.Stat(target)
	if err != nil {
		return pipeline.Result{}, fmt.Errorf("trigger: reprocess target %q: %w", target, err)
	}

	if info.IsDir() {
		scoped := lib
		scoped.Path = target
		return p.Run(ctx, scoped, pipeline.WithForce())
	}

	if !pipeline.IsVideoFile(target) {
		return pipeline.Result{}, fmt.Errorf("trigger: reprocess target %q is not a recognized video file", target)
	}

	return p.RunFile(ctx, lib, target, pipeline.WithForce())
}

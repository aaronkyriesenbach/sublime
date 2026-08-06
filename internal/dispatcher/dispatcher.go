package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

// defaultPollInterval is how often Run wakes to look for newly Pending
// (file, language) pairs when PollInterval isn't set.
const defaultPollInterval = 5 * time.Second

// Dispatcher is the single, process-wide dispatch loop that claims Pending
// (file, language) pairs across every configured Library and hands them to
// Pipeline for the actual Search/Score/Download/Sync/Strip work. It
// coordinates with every Library's Trigger (internal/trigger) only through
// the state store — never a direct in-process handoff — so Trigger's
// registration calls (Run/RunFile/Reprocess) never wait on Dispatcher's
// pace.
type Dispatcher struct {
	// Pipeline runs the actual per-pair work (pipeline.Pipeline.ProcessPending)
	// and supplies WorkerCount for this Dispatcher's worker pool — the same
	// budget that used to be recreated per Library scan is now a single
	// pool shared across every Library.
	Pipeline *pipeline.Pipeline

	// Store is queried each pass for every currently claimable Pending
	// pair. Required.
	Store *store.Store

	// Libraries supplies each configured Library's settings (StripScope in
	// particular) by name, since a claimed pair only carries its owning
	// Library's name, not its full config. A pair whose Library name isn't
	// found here (e.g. removed from config since it was registered) is
	// skipped with a warning log.
	Libraries []domain.Library

	// PollInterval is how often Run wakes to look for newly Pending pairs.
	// Zero uses defaultPollInterval.
	PollInterval time.Duration

	// Logger receives per-pass and per-pair error logging. Defaults to
	// slog.Default() if nil.
	Logger *slog.Logger
}

func (d *Dispatcher) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

func (d *Dispatcher) pollInterval() time.Duration {
	if d.PollInterval > 0 {
		return d.PollInterval
	}
	return defaultPollInterval
}

// RunOnce performs one deterministic dispatch pass: it snapshots every
// (file, language) pair currently in StatusPending across every configured
// Library (store.Store.PendingPairs), then processes that snapshot
// concurrently, bounded by Pipeline.WorkerCount workers, exactly as
// Pipeline.Run's per-file fan-out used to. It returns once every pair from
// that snapshot has been processed (or ctx is cancelled) — it does not
// loop; see Run for the production polling wrapper.
//
// A pair whose gate check finds an already-valid Marker never gets claimed
// at all: it goes straight from Pending to Synced inside
// Pipeline.ProcessPending, unchanged from today.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	pairs, err := d.Store.PendingPairs(ctx)
	if err != nil {
		return fmt.Errorf("dispatcher: listing pending pairs: %w", err)
	}

	librariesByName := make(map[string]domain.Library, len(d.Libraries))
	for _, lib := range d.Libraries {
		librariesByName[lib.Name] = lib
	}

	workerCount := d.Pipeline.WorkerCount
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}

	sem := make(chan struct{}, workerCount)
	var wg sync.WaitGroup

	for _, pair := range pairs {
		lib, ok := librariesByName[pair.LibraryName]
		if !ok {
			d.logger().Warn("dispatcher: pending pair references an unconfigured library; skipping",
				"library", pair.LibraryName, "path", pair.Path)
			continue
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}

		wg.Add(1)
		go func(pair store.PendingPair, lib domain.Library) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := d.Pipeline.ProcessPending(ctx, lib, pair.FileID, pair.ContentHash, pair.Path, pair.Language, pair.Force); err != nil {
				d.logger().Error("dispatcher: processing pending pair failed",
					"library", lib.Name, "path", pair.Path, "language", pair.Language.String(), "error", err)
			}
		}(pair, lib)
	}

	wg.Wait()
	return ctx.Err()
}

// Run wraps RunOnce in a fixed polling-interval loop: it runs one pass
// immediately, then again every PollInterval, until ctx is cancelled. It
// always returns a non-nil error once cancelled (ctx.Err() in the common
// case). A failed pass is logged and doesn't stop the loop — the next tick
// tries again.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.runPass(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(d.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := d.runPass(ctx); err != nil {
				return err
			}
		}
	}
}

// runPass runs one RunOnce pass, logging (rather than propagating) any
// error that isn't ctx cancellation, so a single bad pass doesn't take
// down the whole polling loop.
func (d *Dispatcher) runPass(ctx context.Context) error {
	if err := d.RunOnce(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		d.logger().Error("dispatcher: pass failed", "error", err)
	}
	return nil
}

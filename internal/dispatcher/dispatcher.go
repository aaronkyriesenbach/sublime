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

// ProviderEntry represents a single Provider in the chain with its own
// Pipeline and per-Provider worker pool budget. Each entry in the chain
// is tried in order until one is found that isn't Suspended and has
// spare worker capacity.
type ProviderEntry struct {
	// Name identifies the Provider for logging and tracking (see #76).
	Name string

	// Pipeline runs the actual per-pair work for this Provider.
	Pipeline *pipeline.Pipeline

	// WorkerCount is the number of concurrent workers allocated to this
	// Provider's pool. Zero means use a share of the default budget.
	WorkerCount int
}

// ProviderTier is a named rank within the Provider Chain, holding one or
// more Providers the operator declares equally trustworthy at that priority
// level (CONTEXT.md's Tier entry, ADR 0008). List order inside a Tier is a
// soft preference: the first healthy Provider is always dispatched to in
// preference to a Tier-mate, even when the Tier-mate also has spare capacity.
type ProviderTier struct {
	Providers []ProviderEntry
}

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
	// pool shared across every Library. Used only when Providers is empty
	// (legacy single-provider mode).
	Pipeline *pipeline.Pipeline

	// Providers is the flat Provider Chain (legacy mode, ignores Tier
	// boundaries): each entry is tried in priority order until one is found
	// that isn't Suspended and has spare worker capacity. When non-empty
	// and ProviderTiers is empty, Pipeline is ignored and each entry's own
	// Pipeline is used. Deprecated in favor of ProviderTiers.
	Providers []ProviderEntry

	// ProviderTiers is the Tier-grouped Provider Chain (see ADR 0008):
	// each Tier is tried in priority order, and within a Tier, the first
	// healthy (non-Suspended) Provider with spare capacity is used.
	// Suspension may skip to a Tier-mate but never crosses a Tier boundary:
	// if every Provider in the current Tier is Suspended, the pair stays
	// Pending waiting on that Tier rather than descending to the next.
	// When non-empty, both Pipeline and Providers are ignored.
	ProviderTiers []ProviderTier

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

// suspensionReporter is the optional capability interface a Provider may
// implement to report its own quota-suspension status (see
// opensubtitles.Provider.Suspension and pipeline.NewProduction). Defined
// here, at this package's own wiring seam, so Dispatcher can gate claims
// on it without importing the concrete Provider type.
type suspensionReporter interface {
	Suspension() (resumeAt time.Time, suspended bool)
}

// providerSuspended reports whether the Provider backing d.Pipeline is
// currently Suspended, checked fresh against the Provider's own in-memory
// state on every call — no persisted state of its own. A Provider that
// doesn't implement suspensionReporter is never considered Suspended.
func (d *Dispatcher) providerSuspended() bool {
	return d.pipelineSuspended(d.Pipeline)
}

// pipelineSuspended reports whether the Provider backing p is currently
// Suspended.
func (d *Dispatcher) pipelineSuspended(p *pipeline.Pipeline) bool {
	if p == nil {
		return false
	}
	sr, ok := p.Provider.(suspensionReporter)
	if !ok {
		return false
	}
	_, suspended := sr.Suspension()
	return suspended
}

// RunOnce performs one deterministic dispatch pass: it snapshots every
// (file, language) pair currently in StatusPending across every configured
// Library (store.Store.PendingPairs), then processes that snapshot
// concurrently. When Providers is set, each Provider gets its own worker
// pool and the chain is walked in priority order to find the first
// available Provider for each pair. It returns once every pair from that
// snapshot has been processed (or ctx is cancelled) — it does not loop;
// see Run for the production polling wrapper.
//
// A pair whose gate check finds an already-valid Marker never gets claimed
// at all: it goes straight from Pending to Synced inside
// Pipeline.ProcessPending, unchanged from today.
//
// A pair whose entire Provider Chain is currently Suspended or over
// capacity is left Pending: it's neither claimed nor touched at all, and
// is picked up automatically on a later poll once capacity frees up.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	pairs, err := d.Store.PendingPairs(ctx)
	if err != nil {
		return fmt.Errorf("dispatcher: listing pending pairs: %w", err)
	}

	librariesByName := make(map[string]domain.Library, len(d.Libraries))
	for _, lib := range d.Libraries {
		librariesByName[lib.Name] = lib
	}

	// Tier mode: use tier-grouped provider pools (ADR 0008)
	if len(d.ProviderTiers) > 0 {
		return d.runOnceTierMode(ctx, pairs, librariesByName)
	}

	// Chain mode: use per-provider worker pools (flat chain, legacy)
	if len(d.Providers) > 0 {
		return d.runOnceChainMode(ctx, pairs, librariesByName)
	}

	// Legacy single-pipeline mode
	return d.runOnceLegacyMode(ctx, pairs, librariesByName)
}

// runOnceLegacyMode handles the single-pipeline dispatch mode (Providers empty).
func (d *Dispatcher) runOnceLegacyMode(ctx context.Context, pairs []store.PendingPair, librariesByName map[string]domain.Library) error {
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

		if d.providerSuspended() {
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

// runOnceChainMode handles the Provider Chain dispatch mode (Providers non-empty).
// Each Provider gets its own semaphore, and for each pair we walk the chain
// to find the first Provider that isn't Suspended and has spare capacity.
func (d *Dispatcher) runOnceChainMode(ctx context.Context, pairs []store.PendingPair, librariesByName map[string]domain.Library) error {
	// Build per-provider semaphores
	type providerPool struct {
		entry ProviderEntry
		sem   chan struct{}
	}
	pools := make([]providerPool, len(d.Providers))
	for i, entry := range d.Providers {
		wc := entry.WorkerCount
		if wc <= 0 {
			wc = runtime.NumCPU() / len(d.Providers)
			if wc < 1 {
				wc = 1
			}
		}
		pools[i] = providerPool{
			entry: entry,
			sem:   make(chan struct{}, wc),
		}
	}

	var wg sync.WaitGroup

	for _, pair := range pairs {
		lib, ok := librariesByName[pair.LibraryName]
		if !ok {
			d.logger().Warn("dispatcher: pending pair references an unconfigured library; skipping",
				"library", pair.LibraryName, "path", pair.Path)
			continue
		}

		// Walk the chain to find the first available provider
		var claimed bool
		for i := range pools {
			pool := &pools[i]

			// Skip if this provider is Suspended
			if d.pipelineSuspended(pool.entry.Pipeline) {
				continue
			}

			// Try to acquire a worker slot (non-blocking)
			select {
			case pool.sem <- struct{}{}:
				claimed = true
				wg.Add(1)
				go func(p *pipeline.Pipeline, sem chan struct{}, pair store.PendingPair, lib domain.Library) {
					defer wg.Done()
					defer func() { <-sem }()

					if err := p.ProcessPending(ctx, lib, pair.FileID, pair.ContentHash, pair.Path, pair.Language, pair.Force); err != nil {
						d.logger().Error("dispatcher: processing pending pair failed",
							"library", lib.Name, "path", pair.Path, "language", pair.Language.String(), "error", err)
					}
				}(pool.entry.Pipeline, pool.sem, pair, lib)
			default:
				continue
			}

			if claimed {
				break
			}
		}

		if ctx.Err() != nil {
			break
		}
	}

	wg.Wait()
	return ctx.Err()
}

// tierPool holds a ProviderEntry and its per-provider semaphore.
type tierPool struct {
	entry ProviderEntry
	sem   chan struct{}
}

// runOnceTierMode handles the Tier-grouped Provider Chain dispatch mode
// (ProviderTiers non-empty). Each Provider gets its own semaphore, and for
// each pair we walk Tiers in priority order. Within a Tier, the first
// healthy (non-Suspended) Provider with spare capacity is used. Suspension
// may skip to a Tier-mate but never crosses a Tier boundary: if every
// Provider in the current Tier is Suspended, the pair stays Pending.
func (d *Dispatcher) runOnceTierMode(ctx context.Context, pairs []store.PendingPair, librariesByName map[string]domain.Library) error {
	// Build per-provider semaphores, grouped by tier
	tierPools := make([][]tierPool, len(d.ProviderTiers))
	totalProviders := 0
	for _, tier := range d.ProviderTiers {
		totalProviders += len(tier.Providers)
	}
	if totalProviders == 0 {
		totalProviders = 1
	}

	for i, tier := range d.ProviderTiers {
		tierPools[i] = make([]tierPool, len(tier.Providers))
		for j, entry := range tier.Providers {
			wc := entry.WorkerCount
			if wc <= 0 {
				wc = runtime.NumCPU() / totalProviders
				if wc < 1 {
					wc = 1
				}
			}
			tierPools[i][j] = tierPool{
				entry: entry,
				sem:   make(chan struct{}, wc),
			}
		}
	}

	var wg sync.WaitGroup

pairLoop:
	for _, pair := range pairs {
		lib, ok := librariesByName[pair.LibraryName]
		if !ok {
			d.logger().Warn("dispatcher: pending pair references an unconfigured library; skipping",
				"library", pair.LibraryName, "path", pair.Path)
			continue
		}

		// Walk tiers in priority order
		for tierIdx, pools := range tierPools {
			// Check if ALL providers in this tier are Suspended
			allSuspended := true
			for j := range pools {
				if !d.pipelineSuspended(pools[j].entry.Pipeline) {
					allSuspended = false
					break
				}
			}
			if allSuspended {
				// Every provider in this tier is Suspended; leave pair
				// Pending waiting on this tier (don't descend to next tier)
				d.logger().Debug("dispatcher: tier fully suspended, waiting",
					"tier", tierIdx, "path", pair.Path, "language", pair.Language.String())
				continue pairLoop
			}

			// Walk providers within this tier in list order (soft preference)
			for j := range pools {
				pool := &pools[j]

				// Skip Suspended providers (may skip to Tier-mate)
				if d.pipelineSuspended(pool.entry.Pipeline) {
					continue
				}

				// Try to acquire a worker slot (non-blocking)
				select {
				case pool.sem <- struct{}{}:
					wg.Add(1)
					go func(p *pipeline.Pipeline, name string, sem chan struct{}, pair store.PendingPair, lib domain.Library) {
						defer wg.Done()
						defer func() { <-sem }()

						if err := p.ProcessPending(ctx, lib, pair.FileID, pair.ContentHash, pair.Path, pair.Language, pair.Force); err != nil {
							d.logger().Error("dispatcher: processing pending pair failed",
								"provider", name, "library", lib.Name, "path", pair.Path, "language", pair.Language.String(), "error", err)
						}
					}(pool.entry.Pipeline, pool.entry.Name, pool.sem, pair, lib)
					continue pairLoop
				default:
					continue
				}
			}
			// Tier has healthy providers but no capacity; continue to next tier
		}

		if ctx.Err() != nil {
			break
		}
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

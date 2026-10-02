package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"golang.org/x/text/language"

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

	// state holds the per-Provider worker pools and in-flight tracking that
	// live for the Dispatcher's lifetime, across every pass.
	state poolState
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

// allProvidersAttempted checks whether every configured Provider (across
// all Tiers, or the single Provider in legacy mode, or the chain in flat
// chain mode) is in the pair's attempted-set — i.e., has already searched
// and missed for this (file, language) pair's current cycle.
func (d *Dispatcher) allProvidersAttempted(ctx context.Context, fileID int64, lang language.Tag) bool {
	attempted, err := d.Store.AttemptedProviders(ctx, fileID, lang)
	if err != nil {
		return false
	}
	attemptedSet := make(map[string]bool, len(attempted))
	for _, name := range attempted {
		attemptedSet[name] = true
	}

	if len(d.ProviderTiers) > 0 {
		for _, tier := range d.ProviderTiers {
			for _, entry := range tier.Providers {
				if !attemptedSet[entry.Name] {
					return false
				}
			}
		}
		return true
	}

	if len(d.Providers) > 0 {
		for _, entry := range d.Providers {
			if !attemptedSet[entry.Name] {
				return false
			}
		}
		return true
	}

	return attemptedSet["default"]
}

// workerPool is one Provider's long-lived worker budget. It outlives every
// dispatch pass so the Provider's in-flight count is bounded across passes,
// not just within one.
type workerPool struct {
	entry ProviderEntry
	sem   chan struct{}
}

func (p *workerPool) full() bool {
	return len(p.sem) == cap(p.sem)
}

// inFlightKey identifies a (file, language) pair a worker is processing.
type inFlightKey struct {
	fileID int64
	lang   string
}

// poolState holds everything that must persist across dispatch passes. It is
// built lazily on the first pass because Dispatcher is configured by struct
// literal.
type poolState struct {
	once sync.Once

	// legacy is the single pool of legacy mode (Providers and ProviderTiers
	// both empty).
	legacy *workerPool
	// chain holds one pool per Providers entry, in chain order.
	chain []*workerPool
	// tiers holds one pool per Provider, grouped by Tier, in chain order.
	tiers [][]*workerPool

	// wg tracks every worker goroutine across all passes.
	wg sync.WaitGroup

	mu       sync.Mutex
	inFlight map[inFlightKey]struct{}
}

func (d *Dispatcher) initPools() {
	d.state.once.Do(func() {
		d.state.inFlight = make(map[inFlightKey]struct{})

		newPool := func(entry ProviderEntry, share int) *workerPool {
			wc := entry.WorkerCount
			if wc <= 0 {
				wc = runtime.NumCPU() / share
				if wc < 1 {
					wc = 1
				}
			}
			return &workerPool{entry: entry, sem: make(chan struct{}, wc)}
		}

		switch {
		case len(d.ProviderTiers) > 0:
			total := 0
			for _, tier := range d.ProviderTiers {
				total += len(tier.Providers)
			}
			if total == 0 {
				total = 1
			}
			d.state.tiers = make([][]*workerPool, len(d.ProviderTiers))
			for i, tier := range d.ProviderTiers {
				for _, entry := range tier.Providers {
					d.state.tiers[i] = append(d.state.tiers[i], newPool(entry, total))
				}
			}
		case len(d.Providers) > 0:
			for _, entry := range d.Providers {
				d.state.chain = append(d.state.chain, newPool(entry, len(d.Providers)))
			}
		default:
			wc := d.Pipeline.WorkerCount
			if wc <= 0 {
				wc = runtime.NumCPU()
			}
			d.state.legacy = &workerPool{
				entry: ProviderEntry{Name: "default", Pipeline: d.Pipeline},
				sem:   make(chan struct{}, wc),
			}
		}
	})
}

// RunOnce performs one deterministic dispatch pass: it snapshots every
// (file, language) pair currently in StatusPending across every configured
// Library (store.Store.PendingPairs), then processes that snapshot
// concurrently. When Providers is set, each Provider gets its own worker
// pool and the chain is walked in priority order to find the first
// available Provider for each pair. It returns once every worker started by
// the pass has finished (or ctx is cancelled) — it does not loop; see Run
// for the production polling wrapper, which uses the non-blocking
// dispatchPass instead.
//
// In legacy single-pipeline mode, RunOnce waits for worker capacity so the
// whole snapshot is processed.
//
// A pair whose gate check finds an already-valid Marker never gets claimed
// at all: it goes straight from Pending to Synced inside
// Pipeline.ProcessPending, unchanged from today.
//
// A pair whose entire Provider Chain is currently Suspended or over
// capacity is left Pending: it's neither claimed nor touched at all, and
// is picked up automatically on a later poll once capacity frees up.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	err := d.dispatchPass(ctx, true)
	d.state.wg.Wait()
	if err != nil {
		return err
	}
	return ctx.Err()
}

// dispatchPass snapshots the Pending pairs and hands each one to a worker
// from the first eligible Provider's long-lived pool, then returns without
// waiting for those workers: a long-running pair must not stop the next poll
// from dispatching other pairs to Providers with spare capacity. Workers are
// tracked in d.state.wg; waitForCapacity makes legacy mode block for a free
// worker instead of leaving the pair Pending.
func (d *Dispatcher) dispatchPass(ctx context.Context, waitForCapacity bool) error {
	d.initPools()

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
		d.dispatchTierMode(ctx, pairs, librariesByName)
		return nil
	}

	// Chain mode: use per-provider worker pools (flat chain, legacy)
	if len(d.Providers) > 0 {
		d.dispatchChainMode(ctx, pairs, librariesByName)
		return nil
	}

	// Legacy single-pipeline mode
	d.dispatchLegacyMode(ctx, pairs, librariesByName, waitForCapacity)
	return nil
}

// beginPair records pair as in flight, returning false if a worker already
// holds it. A pair stays Pending in the store until its worker's claim lands,
// so without this a later pass could dispatch the same pair twice.
func (d *Dispatcher) beginPair(pair store.PendingPair) bool {
	key := inFlightKey{fileID: pair.FileID, lang: pair.Language.String()}
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	if _, busy := d.state.inFlight[key]; busy {
		return false
	}
	d.state.inFlight[key] = struct{}{}
	return true
}

func (d *Dispatcher) endPair(pair store.PendingPair) {
	key := inFlightKey{fileID: pair.FileID, lang: pair.Language.String()}
	d.state.mu.Lock()
	delete(d.state.inFlight, key)
	d.state.mu.Unlock()
}

// startWorker runs pair on pool's Provider in a tracked goroutine. The caller
// must already hold a slot in pool.sem and have registered pair via beginPair.
func (d *Dispatcher) startWorker(ctx context.Context, pool *workerPool, pair store.PendingPair, lib domain.Library) {
	d.state.wg.Add(1)
	go func() {
		defer d.state.wg.Done()
		defer d.endPair(pair)
		defer func() { <-pool.sem }()

		name := pool.entry.Name
		result := pool.entry.Pipeline.ProcessPending(ctx, lib, pair.FileID, pair.ContentHash, pair.Path, pair.Language, pair.Force, name)
		switch {
		case result.Outcome == pipeline.OutcomeNoCandidateMiss:
			// Legacy mode has one Provider, so its miss is always terminal.
			if d.state.legacy != nil || d.allProvidersAttempted(ctx, pair.FileID, pair.Language) {
				d.markNoCandidate(ctx, lib, pair)
			}
		case result.Err != nil:
			d.logger().Error("dispatcher: processing pending pair failed",
				"provider", name, "library", lib.Name, "path", pair.Path, "language", pair.Language.String(), "error", result.Err)
		}
	}()
}

// dispatchLegacyMode handles the single-pipeline dispatch mode (Providers empty).
func (d *Dispatcher) dispatchLegacyMode(ctx context.Context, pairs []store.PendingPair, librariesByName map[string]domain.Library, waitForCapacity bool) {
	pool := d.state.legacy

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

		if !d.beginPair(pair) {
			continue
		}

		if waitForCapacity {
			select {
			case pool.sem <- struct{}{}:
			case <-ctx.Done():
				d.endPair(pair)
				return
			}
		} else {
			select {
			case pool.sem <- struct{}{}:
			default:
				d.endPair(pair)
				return
			}
		}

		d.startWorker(ctx, pool, pair, lib)
	}
}

// dispatchChainMode handles the Provider Chain dispatch mode (Providers non-empty).
// Each Provider has its own long-lived pool, and for each pair we walk the
// chain to find the first Provider that isn't Suspended and has spare capacity.
func (d *Dispatcher) dispatchChainMode(ctx context.Context, pairs []store.PendingPair, librariesByName map[string]domain.Library) {
	for _, pair := range pairs {
		if ctx.Err() != nil || allFull(d.state.chain) {
			return
		}

		lib, ok := librariesByName[pair.LibraryName]
		if !ok {
			d.logger().Warn("dispatcher: pending pair references an unconfigured library; skipping",
				"library", pair.LibraryName, "path", pair.Path)
			continue
		}

		if !d.beginPair(pair) {
			continue
		}

		var claimed bool
		for _, pool := range d.state.chain {
			if d.pipelineSuspended(pool.entry.Pipeline) {
				continue
			}

			select {
			case pool.sem <- struct{}{}:
				d.startWorker(ctx, pool, pair, lib)
				claimed = true
			default:
			}
			if claimed {
				break
			}
		}
		if !claimed {
			d.endPair(pair)
		}
	}
}

func allFull(pools []*workerPool) bool {
	for _, p := range pools {
		if !p.full() {
			return false
		}
	}
	return true
}

// dispatchTierMode handles the Tier-grouped Provider Chain dispatch mode
// (ProviderTiers non-empty). Each Provider has its own long-lived pool, and
// for each pair we walk Tiers in priority order. Within a Tier, the first
// healthy (non-Suspended, non-already-attempted) Provider with spare
// capacity is used. Suspension may skip to a Tier-mate but never crosses a
// Tier boundary: if every Provider in the current Tier is Suspended, the
// pair stays Pending. When all Providers across all Tiers have been
// attempted, the Dispatcher marks the pair Failed(no_candidate).
func (d *Dispatcher) dispatchTierMode(ctx context.Context, pairs []store.PendingPair, librariesByName map[string]domain.Library) {
	var every []*workerPool
	for _, pools := range d.state.tiers {
		every = append(every, pools...)
	}

pairLoop:
	for _, pair := range pairs {
		if ctx.Err() != nil || allFull(every) {
			return
		}

		lib, ok := librariesByName[pair.LibraryName]
		if !ok {
			d.logger().Warn("dispatcher: pending pair references an unconfigured library; skipping",
				"library", pair.LibraryName, "path", pair.Path)
			continue
		}

		if !d.beginPair(pair) {
			continue
		}

		attempted, err := d.Store.AttemptedProviders(ctx, pair.FileID, pair.Language)
		if err != nil {
			d.logger().Error("dispatcher: reading attempted providers",
				"library", lib.Name, "path", pair.Path, "language", pair.Language.String(), "error", err)
			d.endPair(pair)
			continue
		}
		attemptedSet := make(map[string]bool, len(attempted))
		for _, name := range attempted {
			attemptedSet[name] = true
		}

		for tierIdx, pools := range d.state.tiers {
			allSuspended := true
			for _, pool := range pools {
				if !d.pipelineSuspended(pool.entry.Pipeline) {
					allSuspended = false
					break
				}
			}
			if allSuspended {
				d.logger().Debug("dispatcher: tier fully suspended, waiting",
					"tier", tierIdx, "path", pair.Path, "language", pair.Language.String())
				d.endPair(pair)
				continue pairLoop
			}

			for _, pool := range pools {
				if d.pipelineSuspended(pool.entry.Pipeline) || attemptedSet[pool.entry.Name] {
					continue
				}

				select {
				case pool.sem <- struct{}{}:
					d.startWorker(ctx, pool, pair, lib)
					continue pairLoop
				default:
				}
			}
		}

		d.endPair(pair)
	}
}

// markNoCandidate lands pair on Failed(no_candidate) after every Provider has
// missed. The write is fenced on the Content Hash the pair was claimed with, so
// a Changed event during the pass leaves the fresh Pending row alone.
func (d *Dispatcher) markNoCandidate(ctx context.Context, lib domain.Library, pair store.PendingPair) {
	err := d.Store.MarkFailed(ctx, pair.FileID, pair.ContentHash, pair.Language, domain.FailureNoCandidate)
	var stale *store.StaleContentHashError
	switch {
	case errors.As(err, &stale):
		d.logger().Warn("dispatcher: discarding stale no_candidate result: content hash changed while processing",
			"library", lib.Name, "path", pair.Path, "language", pair.Language.String(),
			"expected_hash", stale.Expected, "current_hash", stale.Current)
	case err != nil:
		d.logger().Error("dispatcher: marking failed after provider exhaustion",
			"library", lib.Name, "path", pair.Path, "language", pair.Language.String(), "error", err)
	}
}

// Run wraps dispatchPass in a fixed polling-interval loop: it runs one pass
// immediately, then again every PollInterval, until ctx is cancelled. Passes
// never wait on in-flight pairs, so a long-running Provider call cannot stall
// dispatch to other Providers. On cancellation Run waits for every in-flight
// worker to wind down (each leaves its pair Pending) before returning, and
// always returns a non-nil error (ctx.Err() in the common case). A failed
// pass is logged and doesn't stop the loop — the next tick tries again.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.initPools()
	defer d.state.wg.Wait()

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

// runPass runs one dispatchPass, logging (rather than propagating) any
// error that isn't ctx cancellation, so a single bad pass doesn't take
// down the whole polling loop.
func (d *Dispatcher) runPass(ctx context.Context) error {
	if err := d.dispatchPass(ctx, false); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		d.logger().Error("dispatcher: pass failed", "error", err)
	}
	return nil
}

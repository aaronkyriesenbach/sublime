// Package pipeline orchestrates Sublime's subtitle sync workflow.
//
// This file provides NewProduction, a factory for constructing a real
// Provider per entry in config.Config.ProviderChain (opensubtitles and/or
// subdl), each wrapped in its own Pipeline sharing the same alass Sync
// Engine and FFStripper, instead of today's hardcoded single
// opensubtitles.Provider.
package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/opensubtitles"
	"github.com/aaronkyriesenbach/sublime/internal/provider/subdl"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine/alass"
)

// ProductionConfig holds the configuration needed to construct a
// production-ready Provider Chain.
type ProductionConfig struct {
	// Store is the state store for tracking file processing status.
	Store *store.Store

	// Secrets holds every supported Provider's credentials, read from
	// their designated environment variables (config.LoadProviderSecrets).
	Secrets config.ProviderSecrets

	// ProviderChain is the priority-ordered list of Providers to
	// construct, from config.Config.ProviderChain. A nil or empty chain
	// defaults to a single opensubtitles entry, matching config.Load's own
	// default. Deprecated: use ProviderTiers instead.
	ProviderChain []config.ProviderConfig

	// ProviderTiers is the Tier-grouped Provider list from
	// config.Config.ProviderTiers. When non-empty, ProviderChain is ignored
	// and Tiers are preserved in the returned ProviderTierStatuses.
	ProviderTiers []config.ProviderTier

	// WorkerCount is the fallback number of concurrent workers for a chain
	// entry that doesn't set its own worker_count. Zero means
	// runtime.NumCPU().
	WorkerCount int

	// Logger receives per-file outcome logging. Defaults to slog.Default()
	// if nil — see Pipeline.Logger.
	Logger *slog.Logger
}

// ProviderStatus reports one configured Provider's identity, its own
// Pipeline (wired with that Provider), and, for Providers that implement
// the optional suspensionReporter capability, a callback for its live
// Suspended state (see #58's GET /status requirement). Suspension is nil
// when the underlying Provider doesn't implement that capability at all.
type ProviderStatus struct {
	// Name identifies the Provider for GET /status' providers array and
	// dispatcher.ProviderEntry ordering.
	Name string

	// Pipeline is this Provider's own Pipeline instance, ready to hand to
	// dispatcher.ProviderEntry as part of the chain.
	Pipeline *Pipeline

	// WorkerCount is this Provider's configured worker pool budget, for
	// dispatcher.ProviderEntry.WorkerCount.
	WorkerCount int

	// Suspension reports the Provider's current suspension state; nil if
	// the Provider doesn't implement suspensionReporter.
	Suspension func() (resumeAt time.Time, suspended bool)
}

// ProviderTierStatus holds a Tier's worth of ProviderStatus entries,
// preserving the Tier grouping from config.Config.ProviderTiers for
// dispatcher.ProviderTiers wiring.
type ProviderTierStatus struct {
	Providers []ProviderStatus
}

// suspensionReporter is the optional capability interface a Provider may
// implement to report its own quota-suspension status (see issue #56,
// opensubtitles.Provider.Suspension, and subdl.Provider.Suspension). It's
// defined here, at the wiring seam, rather than on the core
// provider.Provider port, so implementing it stays opt-in.
type suspensionReporter interface {
	Suspension() (resumeAt time.Time, suspended bool)
}

// providerStatusFor builds name's ProviderStatus, type-asserting p against
// suspensionReporter so callers outside this package (e.g. internal/api via
// internal/cli/serve.go) never need to import the concrete Provider type.
func providerStatusFor(name string, p provider.Provider) ProviderStatus {
	status := ProviderStatus{Name: name}
	if sr, ok := p.(suspensionReporter); ok {
		status.Suspension = sr.Suspension
	}
	return status
}

// providerConstructor builds a real provider.Provider from cfg's
// entry-specific settings (worker_count, paid) and every Provider's
// secrets. Adding a new Provider means adding one entry to
// providerConstructors, not branching logic elsewhere in this file.
type providerConstructor func(secrets config.ProviderSecrets, entry config.ProviderConfig) (provider.Provider, error)

// providerConstructors maps a providers.chain name to the constructor for
// its concrete Provider. This is the seam that determines which Provider
// names NewProduction recognizes.
var providerConstructors = map[string]providerConstructor{
	"opensubtitles": func(secrets config.ProviderSecrets, _ config.ProviderConfig) (provider.Provider, error) {
		return opensubtitles.New(opensubtitles.Config{Secrets: secrets.OpenSubtitles})
	},
	"subdl": func(secrets config.ProviderSecrets, entry config.ProviderConfig) (provider.Provider, error) {
		return subdl.New(subdl.Config{APIKey: secrets.SubDL.APIKey, Paid: entry.Paid})
	},
}

// NewProduction constructs one real Provider per entry in cfg.ProviderTiers
// (or cfg.ProviderChain for legacy callers), each wrapped in its own
// Pipeline sharing the same alass Sync Engine and FFStripper. The first
// entry's Pipeline is returned as the primary Pipeline for Trigger's
// registration calls (Run/RunFile/Reprocess never touch Provider — see
// docs/adr/0004-decouple-trigger-and-dispatcher.md — so any chain entry
// works equally well there). The second return value reports every
// configured Provider's identity, own Pipeline, worker count, and live
// suspension status, for wiring into both dispatcher.Providers (chain
// mode) and api.Deps.Providers. The third return value is the same data
// grouped by Tier, for dispatcher.ProviderTiers wiring.
func NewProduction(cfg ProductionConfig) (*Pipeline, []ProviderStatus, []ProviderTierStatus, error) {
	// Use ProviderTiers if provided, otherwise fall back to ProviderChain
	if len(cfg.ProviderTiers) > 0 {
		return newProductionFromTiers(cfg)
	}
	return newProductionFromChain(cfg)
}

func newProductionFromChain(cfg ProductionConfig) (*Pipeline, []ProviderStatus, []ProviderTierStatus, error) {
	chain := cfg.ProviderChain
	if len(chain) == 0 {
		chain = []config.ProviderConfig{{Name: "opensubtitles"}}
	}

	statuses := make([]ProviderStatus, 0, len(chain))
	tierStatuses := make([]ProviderTierStatus, 0, len(chain))
	var primary *Pipeline

	for _, entry := range chain {
		ctor, ok := providerConstructors[entry.Name]
		if !ok {
			return nil, nil, nil, fmt.Errorf("providers.chain: unknown provider %q", entry.Name)
		}

		p, err := ctor(cfg.Secrets, entry)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("creating %s provider: %w", entry.Name, err)
		}

		workerCount := entry.WorkerCount
		if workerCount <= 0 {
			workerCount = cfg.WorkerCount
		}

		pipe := &Pipeline{
			Store:       cfg.Store,
			Provider:    p,
			SyncEngine:  alass.New(),
			Stripper:    strip.NewFFStripper(),
			WorkerCount: workerCount,
			Logger:      cfg.Logger,
		}
		if primary == nil {
			primary = pipe
		}

		status := providerStatusFor(entry.Name, p)
		status.Pipeline = pipe
		status.WorkerCount = workerCount
		statuses = append(statuses, status)
		tierStatuses = append(tierStatuses, ProviderTierStatus{Providers: []ProviderStatus{status}})
	}

	return primary, statuses, tierStatuses, nil
}

func newProductionFromTiers(cfg ProductionConfig) (*Pipeline, []ProviderStatus, []ProviderTierStatus, error) {
	statuses := make([]ProviderStatus, 0)
	tierStatuses := make([]ProviderTierStatus, 0, len(cfg.ProviderTiers))
	var primary *Pipeline

	for _, tier := range cfg.ProviderTiers {
		tierProviders := make([]ProviderStatus, 0, len(tier.Providers))
		for _, entry := range tier.Providers {
			ctor, ok := providerConstructors[entry.Name]
			if !ok {
				return nil, nil, nil, fmt.Errorf("providers.chain: unknown provider %q", entry.Name)
			}

			p, err := ctor(cfg.Secrets, entry)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("creating %s provider: %w", entry.Name, err)
			}

			workerCount := entry.WorkerCount
			if workerCount <= 0 {
				workerCount = cfg.WorkerCount
			}

			pipe := &Pipeline{
				Store:       cfg.Store,
				Provider:    p,
				SyncEngine:  alass.New(),
				Stripper:    strip.NewFFStripper(),
				WorkerCount: workerCount,
				Logger:      cfg.Logger,
			}
			if primary == nil {
				primary = pipe
			}

			status := providerStatusFor(entry.Name, p)
			status.Pipeline = pipe
			status.WorkerCount = workerCount
			statuses = append(statuses, status)
			tierProviders = append(tierProviders, status)
		}
		tierStatuses = append(tierStatuses, ProviderTierStatus{Providers: tierProviders})
	}

	return primary, statuses, tierStatuses, nil
}

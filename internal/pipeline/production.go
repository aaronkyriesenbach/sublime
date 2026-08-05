// Package pipeline orchestrates Sublime's subtitle sync workflow.
//
// This file provides NewProduction, a factory for constructing a Pipeline
// wired to real implementations (OpenSubtitles Provider, alass Sync Engine,
// ffmpeg/ffprobe stripper) instead of the fakes used in unit tests.
package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/provider/opensubtitles"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine/alass"
)

// ProductionConfig holds the configuration needed to construct a
// production-ready Pipeline.
type ProductionConfig struct {
	// Store is the state store for tracking file processing status.
	Store *store.Store

	// Secrets holds the OpenSubtitles API credentials.
	Secrets config.OpenSubtitlesSecrets

	// WorkerCount is the number of concurrent workers for CPU/IO-bound
	// operations. Zero means runtime.NumCPU().
	WorkerCount int

	// Logger receives per-file outcome logging. Defaults to slog.Default()
	// if nil — see Pipeline.Logger.
	Logger *slog.Logger
}

// ProviderStatus reports one configured Provider's identity and, for
// Providers that implement the optional suspensionReporter capability, a
// callback for its live Suspended state (see #58's GET /status
// requirement). Suspension is nil when the underlying Provider doesn't
// implement that capability at all.
type ProviderStatus struct {
	// Name identifies the Provider for GET /status' providers array.
	Name string

	// Suspension reports the Provider's current suspension state; nil if
	// the Provider doesn't implement suspensionReporter.
	Suspension func() (resumeAt time.Time, suspended bool)
}

// suspensionReporter is the optional capability interface a Provider may
// implement to report its own quota-suspension status (see issue #56 and
// opensubtitles.Provider.Suspension). It's defined here, at the wiring
// seam, rather than on the core provider.Provider port, so implementing it
// stays opt-in.
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

// NewProduction constructs a Pipeline wired to real implementations:
// - OpenSubtitles Provider for subtitle retrieval
// - alass Sync Engine for audio-based subtitle alignment
// - FFStripper for embedded stream removal and sidecar management
//
// The returned Pipeline is ready for use in the daemon's serve command.
// The second return value reports each configured Provider's identity and
// live suspension status, for wiring into api.Deps.Providers.
func NewProduction(cfg ProductionConfig) (*Pipeline, []ProviderStatus, error) {
	osProvider, err := opensubtitles.New(opensubtitles.Config{
		Secrets: cfg.Secrets,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating opensubtitles provider: %w", err)
	}

	statuses := []ProviderStatus{providerStatusFor("opensubtitles", osProvider)}

	return &Pipeline{
		Store:       cfg.Store,
		Provider:    osProvider,
		SyncEngine:  alass.New(),
		Stripper:    strip.NewFFStripper(),
		WorkerCount: cfg.WorkerCount,
		Logger:      cfg.Logger,
	}, statuses, nil
}

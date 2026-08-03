// Package pipeline orchestrates Sublime's subtitle sync workflow.
//
// This file provides NewProduction, a factory for constructing a Pipeline
// wired to real implementations (OpenSubtitles Provider, alass Sync Engine,
// ffmpeg/ffprobe stripper) instead of the fakes used in unit tests.
package pipeline

import (
	"fmt"

	"github.com/aaronkyriesenbach/sublime/internal/config"
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
}

// NewProduction constructs a Pipeline wired to real implementations:
// - OpenSubtitles Provider for subtitle retrieval
// - alass Sync Engine for audio-based subtitle alignment
// - FFStripper for embedded stream removal and sidecar management
//
// The returned Pipeline is ready for use in the daemon's serve command.
func NewProduction(cfg ProductionConfig) (*Pipeline, error) {
	provider, err := opensubtitles.New(opensubtitles.Config{
		Secrets: cfg.Secrets,
	})
	if err != nil {
		return nil, fmt.Errorf("creating opensubtitles provider: %w", err)
	}

	return &Pipeline{
		Store:       cfg.Store,
		Provider:    provider,
		SyncEngine:  alass.New(),
		Stripper:    strip.NewFFStripper(),
		WorkerCount: cfg.WorkerCount,
	}, nil
}

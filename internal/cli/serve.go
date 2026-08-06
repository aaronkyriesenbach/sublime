package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/aaronkyriesenbach/sublime/internal/api"
	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/dispatcher"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/trigger"
)

const (
	// defaultServeAddr is the address `serve` binds its HTTP API to when
	// --addr isn't given. Matches defaultAPIAddr's port, so CLI thin
	// clients work against a locally started daemon out of the box.
	defaultServeAddr = ":8080"

	// defaultDBPath is where the state store lives when --db isn't given —
	// see "Core stack & storage" (issue #2): SQLite state under /data.
	defaultDBPath = "/data/sublime.db"

	// defaultDispatchPollInterval is how often the Dispatcher wakes to look
	// for newly Pending (file, language) pairs when --dispatch-poll-interval
	// isn't given.
	defaultDispatchPollInterval = 5 * time.Second

	shutdownTimeout = 10 * time.Second
)

// ServeOptions configures Serve.
type ServeOptions struct {
	ConfigPath string
	DBPath     string
	Addr       string

	// DispatchPollInterval is how often the Dispatcher wakes to look for
	// newly Pending (file, language) pairs. Zero uses
	// defaultDispatchPollInterval.
	DispatchPollInterval time.Duration

	// OnReady, if set, is called once the HTTP listener is bound (before
	// Libraries start their initial scan), with the listener's actual
	// address. Useful for tests binding an ephemeral port ("127.0.0.1:0").
	OnReady func(addr string)

	// Logger receives daemon startup/shutdown and per-Library watch
	// errors. Defaults to slog.Default() if nil.
	Logger *slog.Logger
}

func newServeCommand(configPath *string) *cobra.Command {
	var addr string
	var dbPath string
	var dispatchPollInterval time.Duration

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Sublime daemon: watch libraries and serve the HTTP API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return Serve(ctx, ServeOptions{
				ConfigPath:           *configPath,
				DBPath:               dbPath,
				Addr:                 addr,
				DispatchPollInterval: dispatchPollInterval,
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", defaultServeAddr, "address to bind the HTTP API on")
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "path to the SQLite state store")
	cmd.Flags().DurationVar(&dispatchPollInterval, "dispatch-poll-interval", defaultDispatchPollInterval,
		"how often the Dispatcher looks for newly Pending (file, language) pairs")
	return cmd
}

// Serve is the daemon entrypoint: it loads config, opens the state store,
// wires a production Pipeline, starts a trigger.Watcher per configured
// Library plus one shared dispatcher.Dispatcher, and serves the HTTP API
// (internal/api) until ctx is cancelled. Watchers and the Dispatcher run as
// independent goroutines, coordinating only through the state store — see
// docs/adr/0004-decouple-trigger-and-dispatcher.md.
//
// The HTTP listener is bound and serving before any Library's initial scan
// starts, so /health answers immediately even while a large Library is
// still being scanned.
func Serve(ctx context.Context, opts ServeOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if opts.Addr == "" {
		opts.Addr = defaultServeAddr
	}
	if opts.DBPath == "" {
		opts.DBPath = defaultDBPath
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	st, err := store.Open(opts.DBPath)
	if err != nil {
		return fmt.Errorf("opening state store: %w", err)
	}
	defer func() { _ = st.Close() }()

	secrets := config.LoadProviderSecrets()
	p, providerStatuses, tierStatuses, err := pipeline.NewProduction(pipeline.ProductionConfig{
		Store:         st,
		Secrets:       secrets,
		ProviderTiers: cfg.ProviderTiers,
		Logger:        logger,
	})
	if err != nil {
		return fmt.Errorf("constructing pipeline: %w", err)
	}

	apiProviders := make([]api.ProviderStatusFunc, 0, len(providerStatuses))
	for _, ps := range providerStatuses {
		apiProviders = append(apiProviders, api.ProviderStatusFunc{Name: ps.Name, Status: ps.Suspension})
	}

	providerTiers := make([]dispatcher.ProviderTier, 0, len(tierStatuses))
	for _, ts := range tierStatuses {
		entries := make([]dispatcher.ProviderEntry, 0, len(ts.Providers))
		for _, ps := range ts.Providers {
			entries = append(entries, dispatcher.ProviderEntry{
				Name:        ps.Name,
				Pipeline:    ps.Pipeline,
				WorkerCount: ps.WorkerCount,
			})
		}
		providerTiers = append(providerTiers, dispatcher.ProviderTier{Providers: entries})
	}

	apiServer := api.NewServer(api.Deps{
		Store:     st,
		Libraries: cfg.Libraries,
		Reprocess: func(ctx context.Context, lib domain.Library, target string) error {
			return trigger.Reprocess(ctx, p, lib, target)
		},
		Providers: apiProviders,
		Logger:    logger,
	})
	defer apiServer.Close()

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("binding HTTP API address %q: %w", opts.Addr, err)
	}

	httpServer := &http.Server{Handler: apiServer}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	if opts.OnReady != nil {
		opts.OnReady(listener.Addr().String())
	}
	logger.Info("sublime daemon listening", "addr", listener.Addr().String())

	var wg sync.WaitGroup
	for _, lib := range cfg.Libraries {
		wg.Add(1)
		go func(lib domain.Library) {
			defer wg.Done()
			w := &trigger.Watcher{Pipeline: p, Library: lib, OnRunError: func(path string, err error) {
				logger.Error("watch-triggered run failed", "library", lib.Name, "path", path, "error", err)
			}}
			if err := w.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("library watcher stopped", "library", lib.Name, "error", err)
			}
		}(lib)
	}

	d := &dispatcher.Dispatcher{
		ProviderTiers: providerTiers,
		Store:         st,
		Libraries:     cfg.Libraries,
		PollInterval:  opts.DispatchPollInterval,
		Logger:        logger,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("dispatcher stopped", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("sublime daemon shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}

	wg.Wait()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server error: %w", err)
		}
	default:
	}

	return nil
}

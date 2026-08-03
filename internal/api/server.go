package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

// ReprocessFunc forces target (a file, directory, or Library path) through
// lib's pipeline, bypassing the Marker+Content-Hash gate. It matches
// internal/trigger.Reprocess's signature so the production serve command
// can pass that function directly; tests can inject a fake.
type ReprocessFunc func(ctx context.Context, lib domain.Library, target string) (pipeline.Result, error)

// Deps holds Server's dependencies.
type Deps struct {
	// Store is the state store queried for library/status counts. A nil
	// Store makes /libraries, /status, and /reprocess unusable — only
	// intended for tests that exercise /health alone.
	Store *store.Store

	// Libraries is the full set of configured Libraries, as loaded from
	// config.yaml. Config, not the store, is the source of truth for which
	// Libraries exist and what their target languages are.
	Libraries []domain.Library

	// Reprocess runs a forced reprocess in the background for
	// POST /reprocess. Required for that route to do anything.
	Reprocess ReprocessFunc

	// Logger receives background reprocess outcomes/errors. Defaults to
	// slog.Default() if nil.
	Logger *slog.Logger
}

// Server is Sublime's HTTP API. It implements http.Handler and owns the
// goroutines it spawns for asynchronous /reprocess requests — Close cancels
// and waits for them before returning.
type Server struct {
	mux       *http.ServeMux
	store     *store.Store
	libraries []domain.Library
	reprocess ReprocessFunc
	logger    *slog.Logger

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer builds a Server wired to deps. The returned Server is ready to
// serve immediately; callers must call Close when done to release
// background reprocess goroutines.
func NewServer(deps Deps) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		store:     deps.Store,
		libraries: deps.Libraries,
		reprocess: deps.Reprocess,
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
	}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Close cancels any in-flight background reprocess work and waits for it to
// finish.
func (s *Server) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/libraries", s.handleLibraries)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/reprocess", s.handleReprocess)
	mux.HandleFunc("/", s.handleNotFound)
	s.mux = mux
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "no such route")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "GET required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

package trigger

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
)

// defaultDebounceInterval is how long Watcher waits after the last fsnotify
// event for a path before running it through the pipeline, when
// DebounceInterval isn't set.
const defaultDebounceInterval = 500 * time.Millisecond

// Watcher implements the live half of the Trigger model for one Library: an
// initial full scan on Start, followed by an fsnotify watch over the
// Library's directory tree that feeds new/changed video files through the
// pipeline as they appear. There is no periodic rescanning of
// already-processed files.
type Watcher struct {
	// Pipeline runs the actual sync workflow. Required.
	Pipeline *pipeline.Pipeline

	// Library is the Library to scan and watch. Required.
	Library domain.Library

	// DebounceInterval coalesces bursts of fsnotify events for the same
	// path (e.g. the many Write events a multi-GB file copy produces) into
	// a single pipeline run, fired this long after the last event seen for
	// that path. Zero uses defaultDebounceInterval.
	DebounceInterval time.Duration

	// OnWatching, if set, is called once the initial scan has finished and
	// the fsnotify watch is fully established, before Start blocks waiting
	// for events. Useful for tests and startup logging.
	OnWatching func()

	// OnRunError, if set, is called whenever a pipeline run triggered by a
	// watch event fails. path is empty for fsnotify's own Errors channel.
	OnRunError func(path string, err error)
}

// Start runs an initial pipeline.Pipeline.Run over the Library, then watches
// its directory tree with fsnotify, feeding new/changed video files through
// pipeline.Pipeline.RunFile as they appear. It blocks until ctx is
// cancelled or the watcher hits an unrecoverable error, and always returns a
// non-nil error in that case (ctx.Err() in the common case).
func (w *Watcher) Start(ctx context.Context) error {
	if _, err := w.Pipeline.Run(ctx, w.Library); err != nil {
		return fmt.Errorf("trigger: initial scan of library %q: %w", w.Library.Name, err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("trigger: creating fsnotify watcher: %w", err)
	}
	defer func() { _ = fsw.Close() }()

	if err := addRecursive(fsw, w.Library.Path); err != nil {
		return fmt.Errorf("trigger: watching library %q: %w", w.Library.Name, err)
	}

	d := newDebouncer(w.debounceInterval(), func(path string) {
		if _, err := w.Pipeline.RunFile(ctx, w.Library, path); err != nil && w.OnRunError != nil {
			w.OnRunError(path, err)
		}
	})
	defer d.Stop()

	if w.OnWatching != nil {
		w.OnWatching()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			w.handleEvent(fsw, event, d)
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			if w.OnRunError != nil {
				w.OnRunError("", err)
			}
		}
	}
}

func (w *Watcher) debounceInterval() time.Duration {
	if w.DebounceInterval > 0 {
		return w.DebounceInterval
	}
	return defaultDebounceInterval
}

// handleEvent reacts to a single fsnotify event: newly created directories
// are added to the watch (fsnotify only watches directories
// non-recursively), and video file creates/writes are scheduled for
// (debounced) pipeline processing.
func (w *Watcher) handleEvent(fsw *fsnotify.Watcher, event fsnotify.Event, d *debouncer) {
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			_ = addRecursive(fsw, event.Name)
			return
		}
	}

	if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) {
		return
	}
	if !pipeline.IsVideoFile(event.Name) {
		return
	}

	d.Schedule(event.Name)
}

// addRecursive adds root and every subdirectory under it to fsw. fsnotify
// only watches a directory's immediate children, so the whole tree must be
// added explicitly and newly created subdirectories added as they appear
// (see handleEvent).
func addRecursive(fsw *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return fsw.Add(path)
		}
		return nil
	})
}

// debouncer coalesces repeated Schedule calls for the same path into a
// single fire, run after quiet has elapsed since the last call for that
// path.
type debouncer struct {
	quiet time.Duration
	fire  func(path string)

	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newDebouncer(quiet time.Duration, fire func(path string)) *debouncer {
	return &debouncer{
		quiet:  quiet,
		fire:   fire,
		timers: make(map[string]*time.Timer),
	}
}

func (d *debouncer) Schedule(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, ok := d.timers[path]; ok {
		t.Stop()
	}
	d.timers[path] = time.AfterFunc(d.quiet, func() {
		d.mu.Lock()
		delete(d.timers, path)
		d.mu.Unlock()
		d.fire(path)
	})
}

// Stop cancels every pending timer. Timers already firing when Stop is
// called are not interrupted.
func (d *debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.timers {
		t.Stop()
	}
}

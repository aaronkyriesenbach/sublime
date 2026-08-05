package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/scoring"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

// videoExtensions are the file extensions the pipeline recognizes as video
// files when scanning a Library directory. Lowercase, with leading dot.
var videoExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".m4v":  true,
	".mov":  true,
	".webm": true,
	".wmv":  true,
}

// Pipeline orchestrates the end-to-end subtitle sync workflow for a Library.
type Pipeline struct {
	Store      *store.Store
	Provider   provider.Provider
	SyncEngine syncengine.SyncEngine
	Stripper   Stripper

	// WorkerCount is the number of concurrent workers for CPU/IO-bound
	// operations. Zero means runtime.NumCPU().
	WorkerCount int

	// Logger receives per-file outcome logging (failures and no-candidate
	// terminal states) as Run/RunFile process a Library. Defaults to
	// slog.Default() if nil.
	Logger *slog.Logger
}

// logger returns p.Logger, or slog.Default() if it isn't set.
func (p *Pipeline) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// logStatusChange emits the single uniform "status changed" log line for a
// Sync Status transition (see CONTEXT.md's Sync Status entry) — library,
// path, language, from, to, and, only when landing on Failed, a reason.
// Level is INFO for every transition except landing on Failed: WARN when
// reason is FailureNoCandidate (a normal, expected outcome — no Provider
// had a good enough match), ERROR for the other three failure reasons
// (all indicating something broke rather than simply not matching).
func (p *Pipeline) logStatusChange(
	ctx context.Context,
	lib domain.Library,
	videoPath string,
	lang language.Tag,
	from, to domain.SyncStatus,
	reason domain.FailureReason,
	cause error,
) {
	args := []any{
		"library", lib.Name,
		"path", videoPath,
		"language", lang.String(),
		"from", string(from),
		"to", string(to),
	}

	level := slog.LevelInfo
	if to == domain.StatusFailed {
		args = append(args, "reason", string(reason))
		if cause != nil {
			args = append(args, "error", cause)
		}
		if reason == domain.FailureNoCandidate {
			level = slog.LevelWarn
		} else {
			level = slog.LevelError
		}
	}

	p.logger().Log(ctx, level, "status changed", args...)
}

// markFailed transitions fileID's (file, language) pair to StatusFailed
// with reason in the store and, on success, emits the uniform "status
// changed" log for the In Progress -> Failed transition. Every processFile
// failure path funnels through here so a Failed landing always gets
// exactly one log line.
func (p *Pipeline) markFailed(
	ctx context.Context,
	lib domain.Library,
	videoPath string,
	lang language.Tag,
	fileID int64,
	reason domain.FailureReason,
	cause error,
) error {
	if err := p.Store.MarkFailed(ctx, fileID, lang, reason); err != nil {
		return err
	}
	p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusInProgress, domain.StatusFailed, reason, cause)
	return nil
}

// Stripper is the subset of strip.FFStripper the pipeline needs, extracted
// as an interface so tests can inject a fake that doesn't shell out to
// ffprobe/ffmpeg.
type Stripper interface {
	// StripEmbedded removes videoPath's embedded subtitle streams matching
	// scope, mutating the video file in place when it removes anything. The
	// pipeline calls this before computing the Content Hash used for the
	// Marker and the store, so that hash reflects the file's settled
	// post-Strip state — see CONTEXT.md's Content Hash entry.
	StripEmbedded(ctx context.Context, videoPath string, scope domain.StripScope, lang language.Tag) ([]int, error)

	Swap(
		ctx context.Context,
		videoPath string,
		lang language.Tag,
		scope domain.StripScope,
		hash media.ContentHash,
		sidecarExt string,
		sidecarContent []byte,
	) (strip.SwapResult, error)
}

// processOutcome is the result of processing a single (file, language) pair.
type processOutcome int

const (
	// outcomeSynced means a new sidecar was written.
	outcomeSynced processOutcome = iota
	// outcomeSkipped means a valid Marker already existed.
	outcomeSkipped
	// outcomeTerminal means a non-error terminal state was recorded (e.g.
	// no_candidate) — the file won't be retried until content changes.
	outcomeTerminal
	// outcomeFailed means an error occurred.
	outcomeFailed
)

// RunOption configures how Run or RunFile process files.
type RunOption func(*runConfig)

type runConfig struct {
	force bool
}

// WithForce bypasses the Marker+Content-Hash gate so every (file, language)
// pair is searched, downloaded, synced, and stripped again regardless of
// prior state. Used for manual reprocessing; normal scans and
// watch-triggered runs should leave it unset so already-synced files are
// skipped.
func WithForce() RunOption {
	return func(c *runConfig) { c.force = true }
}

// Result summarizes what the pipeline did for one Library run.
type Result struct {
	// FilesScanned is the total number of video files found in the Library.
	FilesScanned int

	// Synced counts (file, language) pairs that produced a new sidecar.
	Synced int

	// Skipped counts (file, language) pairs where a valid Marker already
	// existed — no fetch needed.
	Skipped int

	// NoCandidate counts (file, language) pairs where no Candidate cleared
	// the scoring cutoff.
	NoCandidate int

	// Failed counts (file, language) pairs that failed with an error.
	Failed int

	// Errors collects the first error per failed (file, language) pair,
	// keyed by "path:lang".
	Errors map[string]error
}

// Run processes every video file in lib, syncing subtitles for each of
// lib.Languages. Files are streamed to workers as the Library's directory
// walk discovers them — each is registered (Found or Changed — see
// CONTEXT.md) and fanned out to Pending the instant it's seen, rather than
// waiting for the whole tree to be walked first. It returns after all
// files have been processed (or ctx is cancelled).
func (p *Pipeline) Run(ctx context.Context, lib domain.Library, opts ...RunOption) (Result, error) {
	paths, walkErrCh := p.scanLibraryStream(ctx, lib.Path)
	return p.runFiles(ctx, lib, paths, walkErrCh, opts...)
}

// RunFile processes a single video file within lib for each of
// lib.Languages, without scanning the rest of the Library. It's the
// entrypoint triggers use for fsnotify watch events and single-file manual
// reprocessing, where a full Library scan would be wasteful. It applies the
// same Found-vs-Changed classification as Run (see CONTEXT.md).
func (p *Pipeline) RunFile(ctx context.Context, lib domain.Library, videoPath string, opts ...RunOption) (Result, error) {
	paths := make(chan string, 1)
	paths <- videoPath
	close(paths)
	return p.runFiles(ctx, lib, paths, nil, opts...)
}

// runFiles registers each path read from videoPaths (Found or Changed — see
// registerFile) and fans its languages out to a worker pool bounded by
// p.WorkerCount, as paths stream in. Registration happens on this method's
// own goroutine and is never blocked by worker availability: a per-file
// registration (and its Pending rows) completes as soon as the file is
// read from videoPaths, regardless of how backed up the workers are, so a
// slow worker pool never delays a later file from being registered. Actual
// per-language work is throttled to WorkerCount concurrent goroutines via
// sem; walkErrCh, if non-nil, carries an error from the goroutine feeding
// videoPaths (nil once it's exhausted successfully).
func (p *Pipeline) runFiles(
	ctx context.Context,
	lib domain.Library,
	videoPaths <-chan string,
	walkErrCh <-chan error,
	opts ...RunOption,
) (Result, error) {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	workerCount := p.WorkerCount
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}

	type outcome struct {
		videoPath string
		lang      language.Tag
		outcome   processOutcome
		err       error
	}

	outcomes := make(chan outcome)
	sem := make(chan struct{}, workerCount)
	var wg sync.WaitGroup

	resultCh := make(chan Result, 1)
	go func() {
		result := Result{Errors: make(map[string]error)}
		for o := range outcomes {
			switch o.outcome {
			case outcomeFailed:
				result.Failed++
				if o.err != nil {
					result.Errors[fmt.Sprintf("%s:%s", o.videoPath, o.lang)] = o.err
				}
			case outcomeSkipped:
				result.Skipped++
			case outcomeSynced:
				result.Synced++
			case outcomeTerminal:
				result.NoCandidate++
			}
		}
		resultCh <- result
	}()

	filesScanned := 0
	for videoPath := range videoPaths {
		filesScanned++

		file, err := p.registerFile(ctx, lib, videoPath, cfg.force)
		if err != nil {
			registerErr := fmt.Errorf("registering file: %w", err)
			p.logger().Error("registering file failed", "library", lib.Name, "path", videoPath, "error", err)
			for _, lang := range lib.Languages {
				wg.Add(1)
				go func() {
					defer wg.Done()
					outcomes <- outcome{videoPath: videoPath, lang: lang, outcome: outcomeFailed, err: registerErr}
				}()
			}
			continue
		}

		for _, lang := range lib.Languages {
			wg.Add(1)
			go func() {
				defer wg.Done()

				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					p.logger().Error("processing cancelled", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", ctx.Err())
					outcomes <- outcome{videoPath: videoPath, lang: lang, outcome: outcomeFailed, err: ctx.Err()}
					return
				}
				defer func() { <-sem }()

				select {
				case <-ctx.Done():
					p.logger().Error("processing cancelled", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", ctx.Err())
					outcomes <- outcome{videoPath: videoPath, lang: lang, outcome: outcomeFailed, err: ctx.Err()}
					return
				default:
				}

				res, syncErr := p.processFile(ctx, lib, file, videoPath, lang, cfg.force)
				outcomes <- outcome{videoPath: videoPath, lang: lang, outcome: res, err: syncErr}
			}()
		}
	}

	var walkErr error
	if walkErrCh != nil {
		walkErr = <-walkErrCh
	}

	wg.Wait()
	close(outcomes)
	result := <-resultCh
	result.FilesScanned = filesScanned

	if walkErr != nil {
		return result, fmt.Errorf("pipeline: scanning library %q: %w", lib.Name, walkErr)
	}

	return result, nil
}

// IsVideoFile reports whether path has a file extension the pipeline
// recognizes as a video file.
func IsVideoFile(path string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(path))]
}

// scanLibraryStream walks root on its own goroutine and streams the
// absolute paths of every video file found on the returned channel as the
// walk discovers them, closing it once the walk finishes. The returned
// error channel receives at most one error (a walk failure, including ctx
// cancellation) and is always closed; a nil-or-not-yet-received read after
// the paths channel closes means the walk completed successfully.
func (p *Pipeline) scanLibraryStream(ctx context.Context, root string) (<-chan string, <-chan error) {
	paths := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		defer close(errCh)
		defer close(paths)

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !IsVideoFile(path) {
				return nil
			}
			select {
			case paths <- path:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			errCh <- err
		}
	}()

	return paths, errCh
}

// registerFile classifies videoPath as Found or Changed (see CONTEXT.md)
// the instant it's seen — whether by the bulk scan or a single-file
// trigger — and logs exactly one "file found" or "file changed" line for
// it, never one per language:
//
//   - Found: videoPath has never been tracked before. It's registered and
//     a Pending row is fanned out for each of lib.Languages.
//   - Changed: videoPath is already tracked and either its Content Hash
//     differs from its last-recorded value, or force is set (a manual
//     reprocess request). Its existing language states are reset to
//     Pending in place.
//   - Neither: videoPath is already tracked, its hash is unchanged, and
//     force isn't set — nothing is logged. lib.Languages still get Pending
//     rows fanned out for any newly configured language absent from the
//     file's existing rows.
func (p *Pipeline) registerFile(ctx context.Context, lib domain.Library, videoPath string, force bool) (domain.File, error) {
	hash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		return domain.File{}, fmt.Errorf("computing content hash: %w", err)
	}

	file, observation, err := p.Store.ObserveFileHash(ctx, lib.Name, videoPath, string(hash))
	if err != nil {
		return domain.File{}, fmt.Errorf("observing file hash: %w", err)
	}

	switch {
	case observation == store.FileHashNew:
		p.logger().Info("file found", "library", lib.Name, "path", videoPath)
	case observation == store.FileHashChanged:
		p.logger().Info("file changed", "library", lib.Name, "path", videoPath)
	case force:
		p.logger().Info("file changed", "library", lib.Name, "path", videoPath)
		if err := p.Store.ResetToPending(ctx, file.ID); err != nil {
			return domain.File{}, fmt.Errorf("resetting file to pending: %w", err)
		}
	}

	for _, lang := range lib.Languages {
		if err := p.Store.EnsureLanguage(ctx, file.ID, lang); err != nil {
			return domain.File{}, fmt.Errorf("ensuring language state: %w", err)
		}
	}

	return file, nil
}

// processFile handles a single (video, language) pair: gate check, search,
// score, download, sync, write sidecar, strip, and record outcome. file
// must already be registered (see registerFile) with a valid ID and
// Content Hash for videoPath.
func (p *Pipeline) processFile(
	ctx context.Context,
	lib domain.Library,
	file domain.File,
	videoPath string,
	lang language.Tag,
	force bool,
) (processOutcome, error) {
	hash := media.ContentHash(file.ContentHash)

	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	if !force {
		needsFetch, err := strip.NeedsFetch(dir, stem, lang, hash)
		if err != nil {
			p.logger().Error("checking marker gate failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
			return outcomeFailed, fmt.Errorf("checking marker gate: %w", err)
		}
		if !needsFetch {
			if err := p.Store.MarkSynced(ctx, file.ID, lang); err != nil {
				p.logger().Error("marking already-synced file failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
				return outcomeFailed, fmt.Errorf("marking already-synced file: %w", err)
			}
			p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusPending, domain.StatusSynced, domain.FailureNone, nil)
			return outcomeSkipped, nil
		}
	}

	if err := p.Store.MarkInProgress(ctx, file.ID, lang); err != nil {
		p.logger().Error("marking in progress failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
		return outcomeFailed, fmt.Errorf("marking in progress: %w", err)
	}
	p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusPending, domain.StatusInProgress, domain.FailureNone, nil)

	query, queryErr := queryFromPath(videoPath, lang)
	if queryErr != nil {
		p.logger().Warn("filename metadata unparseable; falling back to hash-only search",
			"library", lib.Name, "path", videoPath, "language", lang.String(), "error", queryErr)
	}

	candidates, err := p.Provider.Search(ctx, query)
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureRetrievalFailed, err); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("searching provider: %w", err)
	}

	info, parseErr := scoring.Parse(filepath.Base(videoPath))
	if parseErr != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, parseErr); markErr != nil {
			return outcomeFailed, errors.Join(parseErr, markErr)
		}
		return outcomeFailed, fmt.Errorf("parsing video filename: %w", parseErr)
	}

	best, ok := scoring.Select(info, candidates)
	if !ok {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureNoCandidate, nil); markErr != nil {
			return outcomeFailed, markErr
		}
		return outcomeTerminal, nil
	}

	subtitleContent, err := p.Provider.Download(ctx, best)
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureRetrievalFailed, err); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("downloading candidate: %w", err)
	}

	syncedContent, err := p.syncSubtitle(ctx, videoPath, subtitleContent)
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureSyncFailed, err); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("syncing subtitle: %w", err)
	}

	sidecarExt := ".srt"
	codec, ok := marker.CodecFor(sidecarExt)
	if !ok {
		noCodecErr := fmt.Errorf("no marker codec for %s", sidecarExt)
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, noCodecErr); markErr != nil {
			return outcomeFailed, markErr
		}
		return outcomeFailed, noCodecErr
	}

	removedStreams, err := p.Stripper.StripEmbedded(ctx, videoPath, lib.StripScope, lang)
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("stripping embedded streams: %w", err)
	}

	// StripEmbedded's ffmpeg remux genuinely changes the video's bytes when
	// it removes a stream, so the hash bound into the Marker and store must
	// reflect the settled post-Strip file — the pre-Strip hash captured at
	// the top of this step would otherwise misread Sublime's own edit as an
	// external content change on the next scan (see CONTEXT.md's Content
	// Hash entry).
	if len(removedStreams) > 0 {
		correctedHash, err := media.ComputeContentHash(videoPath)
		if err != nil {
			if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
				return outcomeFailed, errors.Join(err, markErr)
			}
			return outcomeFailed, fmt.Errorf("recomputing content hash after strip: %w", err)
		}
		hash = correctedHash

		if err := p.Store.UpdateContentHash(ctx, file.ID, string(hash)); err != nil {
			return outcomeFailed, fmt.Errorf("recording corrected content hash: %w", err)
		}
	}

	markedContent, err := codec.Write(syncedContent, marker.Marker{ContentHash: hash})
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("embedding marker: %w", err)
	}

	if _, err := p.Stripper.Swap(ctx, videoPath, lang, lib.StripScope, hash, sidecarExt, markedContent); err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("swapping sidecar: %w", err)
	}

	if err := p.Store.MarkSynced(ctx, file.ID, lang); err != nil {
		p.logger().Error("marking synced failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
		return outcomeFailed, fmt.Errorf("marking synced: %w", err)
	}
	p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusInProgress, domain.StatusSynced, domain.FailureNone, nil)

	return outcomeSynced, nil
}

// syncSubtitle writes candidateContent to a temp file, runs the SyncEngine,
// reads the output, and cleans up. The temp files are written to the OS
// temp directory, not alongside the video, to avoid cluttering the library.
func (p *Pipeline) syncSubtitle(ctx context.Context, videoPath string, candidateContent []byte) ([]byte, error) {
	_ = ctx // SyncEngine.Sync doesn't take ctx yet; kept for future use

	tmpDir, err := os.MkdirTemp("", "sublime-sync-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	inputPath := filepath.Join(tmpDir, "input.srt")
	outputPath := filepath.Join(tmpDir, "output.srt")

	if err := os.WriteFile(inputPath, candidateContent, 0o644); err != nil {
		return nil, fmt.Errorf("writing temp subtitle: %w", err)
	}

	if _, err := p.SyncEngine.Sync(videoPath, inputPath, outputPath); err != nil {
		return nil, fmt.Errorf("sync engine: %w", err)
	}

	syncedContent, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("reading synced output: %w", err)
	}

	return syncedContent, nil
}

// queryFromPath builds a provider.Query from a video's path and target
// language. When the filename can't be parsed (see scoring.Parse), it
// returns a degraded, path-only Query — hash search can still work off
// Path alone — plus the parse error, for the caller to log.
func queryFromPath(videoPath string, lang language.Tag) (provider.Query, error) {
	info, err := scoring.Parse(filepath.Base(videoPath))
	if err != nil {
		return provider.Query{Language: lang, Path: videoPath}, err
	}
	return provider.Query{
		Title:    info.Title,
		Year:     info.Year,
		Season:   info.Season,
		Episode:  info.Episode,
		Language: lang,
		Path:     videoPath,
	}, nil
}

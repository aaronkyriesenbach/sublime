package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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

// Pipeline orchestrates the end-to-end subtitle sync workflow shared by
// Trigger (registration: Run/RunFile) and Dispatcher (claim-and-fetch:
// ProcessPending) — see docs/adr/0004-decouple-trigger-and-dispatcher.md.
type Pipeline struct {
	Store      *store.Store
	Provider   provider.Provider
	SyncEngine syncengine.SyncEngine
	Stripper   Stripper

	// WorkerCount is the number of concurrent workers a Dispatcher should
	// use when calling ProcessPending concurrently for this Pipeline. Zero
	// means runtime.NumCPU(). Unused by Run/RunFile, which no longer fan
	// out any per-pair work themselves.
	WorkerCount int

	// Logger receives per-file registration logging (Run/RunFile) and
	// per-pair outcome logging (ProcessPending). Defaults to slog.Default()
	// if nil.
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

// markPending resets fileID's (file, language) pair back to StatusPending
// via the store's single-language reset and, on success, emits the
// uniform "status changed" log for the In Progress -> Pending transition.
// Used when a Provider reports QuotaExhaustedError: the attempt didn't
// fail in any way that reflects on the file itself, so it's requeued
// rather than landing on Failed, and logged as an ordinary Info-level
// landing with no FailureReason — the "something's off" signal already
// lives in the Provider's own Suspended log line.
func (p *Pipeline) markPending(
	ctx context.Context,
	lib domain.Library,
	videoPath string,
	lang language.Tag,
	fileID int64,
) error {
	if err := p.Store.ResetLanguageToPending(ctx, fileID, lang); err != nil {
		return err
	}
	p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusInProgress, domain.StatusPending, domain.FailureNone, nil)
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

// ProcessOutcome describes what happened when processing a (file, language)
// pair, so the Dispatcher can make informed chain-advance decisions.
type ProcessOutcome int

const (
	// OutcomeSynced means a new sidecar was written.
	OutcomeSynced ProcessOutcome = iota
	// OutcomeSkipped means a valid Marker already existed or the claim was lost.
	OutcomeSkipped
	// OutcomeNoCandidateMiss means Search found no Candidate clearing the
	// scoring cutoff. The pair has been reset to Pending via
	// Store.RecordProviderMiss — the Dispatcher should check whether all
	// Providers are exhausted and mark Failed(no_candidate) if so.
	OutcomeNoCandidateMiss
	// OutcomePending means the pair was reset to StatusPending after a
	// provider.QuotaExhaustedError — it will be retried on a future run.
	OutcomePending
	// OutcomeFailed means a non-recoverable failure occurred (not
	// no_candidate — that's OutcomeNoCandidateMiss).
	OutcomeFailed
)

// ProcessResult holds the outcome of ProcessPending plus any error, so the
// Dispatcher can distinguish a no-candidate miss from other outcomes and
// make the chain-exhaustion decision itself.
type ProcessResult struct {
	Outcome ProcessOutcome
	Err     error
}

// RunOption configures how Run or RunFile process files.
type RunOption func(*runConfig)

type runConfig struct {
	force bool
}

// WithForce marks every (file, language) pair Run or RunFile registers as
// force-reset (store.Store.ResetToPendingForced) instead of an ordinary
// reset, so the Dispatcher that later claims and processes them bypasses
// the Marker+Content-Hash gate regardless of whether a still-valid Marker
// exists. Used for manual reprocessing; normal scans and watch-triggered
// runs should leave it unset so already-synced files are left for the
// Dispatcher's gate to skip.
func WithForce() RunOption {
	return func(c *runConfig) { c.force = true }
}

// RegistrationResult summarizes what Run or RunFile registered for one
// Trigger pass: the files it scanned and how each was classified (see
// CONTEXT.md's Found and Changed entries). It says nothing about whether
// any (file, language) pair was actually synced — that outcome is now
// entirely the Dispatcher's, observable only via the state store (e.g.
// GET /status) and the per-transition "status changed" logs, never
// returned synchronously to a Trigger caller. See
// docs/adr/0004-decouple-trigger-and-dispatcher.md.
type RegistrationResult struct {
	// FilesScanned is the total number of video files found in the Library.
	FilesScanned int

	// Found counts files that had never been tracked before this pass.
	Found int

	// Changed counts already-tracked files whose Content Hash differed
	// from its last-recorded value, or that were targeted by a forced
	// reprocess request.
	Changed int
}

// Run registers every video file in lib, classifying each as Found or
// Changed (see CONTEXT.md) and fanning out a Pending Sync Status for each
// of lib.Languages the instant it's seen, rather than waiting for the
// whole tree to be walked first. It returns as soon as registration for
// every discovered file has completed (or ctx is cancelled) — it never
// waits on any Dispatcher claiming or processing the Pending rows it just
// wrote.
func (p *Pipeline) Run(ctx context.Context, lib domain.Library, opts ...RunOption) (RegistrationResult, error) {
	paths, walkErrCh := p.scanLibraryStream(ctx, lib.Path)
	return p.runFiles(ctx, lib, paths, walkErrCh, opts...)
}

// RunFile registers a single video file within lib for each of
// lib.Languages, without scanning the rest of the Library. It's the
// entrypoint triggers use for fsnotify watch events and single-file manual
// reprocessing, where a full Library scan would be wasteful. It applies the
// same Found-vs-Changed classification as Run (see CONTEXT.md) and returns
// just as promptly, without waiting on the Dispatcher.
func (p *Pipeline) RunFile(ctx context.Context, lib domain.Library, videoPath string, opts ...RunOption) (RegistrationResult, error) {
	paths := make(chan string, 1)
	paths <- videoPath
	close(paths)
	return p.runFiles(ctx, lib, paths, nil, opts...)
}

// runFiles registers each path read from videoPaths (Found or Changed — see
// registerFile) as it streams in, tallying a RegistrationResult; it never
// fans work out to any worker pool — that's the Dispatcher's job, running
// independently and coordinating only through the rows this method writes
// to the store. walkErrCh, if non-nil, carries an error from the goroutine
// feeding videoPaths (nil once it's exhausted successfully).
func (p *Pipeline) runFiles(
	ctx context.Context,
	lib domain.Library,
	videoPaths <-chan string,
	walkErrCh <-chan error,
	opts ...RunOption,
) (RegistrationResult, error) {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	var result RegistrationResult

scan:
	for {
		select {
		case videoPath, ok := <-videoPaths:
			if !ok {
				break scan
			}
			result.FilesScanned++

			_, classification, err := p.registerFile(ctx, lib, videoPath, cfg.force)
			if err != nil {
				p.logger().Error("registering file failed", "library", lib.Name, "path", videoPath, "error", err)
				continue
			}

			switch classification {
			case fileFound:
				result.Found++
			case fileChanged:
				result.Changed++
			}
		case <-ctx.Done():
			break scan
		}
	}

	var walkErr error
	if walkErrCh != nil {
		walkErr = <-walkErrCh
	}

	if walkErr != nil {
		return result, fmt.Errorf("pipeline: scanning library %q: %w", lib.Name, walkErr)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	return result, nil
}

// IsVideoFile reports whether path is a recognized-extension video file that isn't one of Strip's own stray remux temp files (strip.IsStrayTempFile).
func IsVideoFile(path string) bool {
	if !videoExtensions[strings.ToLower(filepath.Ext(path))] {
		return false
	}
	return !strip.IsStrayTempFile(filepath.Base(path))
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

// fileClassification reports how registerFile classified a video path —
// see CONTEXT.md's Found and Changed entries.
type fileClassification int

const (
	// fileUnchanged means videoPath was already tracked, its hash is
	// unchanged, and force wasn't set.
	fileUnchanged fileClassification = iota
	// fileFound means videoPath had never been tracked before.
	fileFound
	// fileChanged means videoPath was already tracked but its Content Hash
	// differs from its last-recorded value, or force is set.
	fileChanged
)

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
//     Pending in place — force-reset via store.Store.ResetToPendingForced
//     so a future Dispatcher claim bypasses the Marker+Content-Hash gate,
//     plain store.Store.ResetToPending otherwise.
//   - Neither: videoPath is already tracked, its hash is unchanged, and
//     force isn't set — nothing is logged. lib.Languages still get Pending
//     rows fanned out for any newly configured language absent from the
//     file's existing rows.
func (p *Pipeline) registerFile(ctx context.Context, lib domain.Library, videoPath string, force bool) (domain.File, fileClassification, error) {
	hash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		return domain.File{}, fileUnchanged, fmt.Errorf("computing content hash: %w", err)
	}

	file, observation, err := p.Store.ObserveFileHash(ctx, lib.Name, videoPath, string(hash))
	if err != nil {
		return domain.File{}, fileUnchanged, fmt.Errorf("observing file hash: %w", err)
	}

	classification := fileUnchanged
	switch {
	case observation == store.FileHashNew:
		classification = fileFound
		p.logger().Info("file found", "library", lib.Name, "path", videoPath)
	case observation == store.FileHashChanged:
		classification = fileChanged
		p.logger().Info("file changed", "library", lib.Name, "path", videoPath)
	case force:
		classification = fileChanged
		p.logger().Info("file changed", "library", lib.Name, "path", videoPath)
		if err := p.Store.ResetToPendingForced(ctx, file.ID); err != nil {
			return domain.File{}, fileUnchanged, fmt.Errorf("resetting file to pending: %w", err)
		}
	}

	for _, lang := range lib.Languages {
		if err := p.Store.EnsureLanguage(ctx, file.ID, lang); err != nil {
			return domain.File{}, fileUnchanged, fmt.Errorf("ensuring language state: %w", err)
		}
	}

	return file, classification, nil
}

// ProcessPending performs the actual claim-and-fetch work for a single
// Pending (file, language) pair that a Dispatcher has selected from
// store.Store.PendingPairs: the Marker+Content-Hash gate check, the atomic
// claim (Pending -> In Progress), Search/Score/Download/Sync/Strip, and
// outcome recording — unchanged from what Run/RunFile used to do inline,
// just called from the Dispatcher's own loop instead. fileID, contentHash,
// and videoPath identify an already-registered file (see registerFile);
// force bypasses the gate, matching store.PendingPair.Force; providerName
// identifies which Provider is attempting the pair (for no-candidate miss
// recording — see docs/adr/0008-tiered-provider-chain.md). See
// docs/adr/0004-decouple-trigger-and-dispatcher.md.
func (p *Pipeline) ProcessPending(ctx context.Context, lib domain.Library, fileID int64, contentHash, videoPath string, lang language.Tag, force bool, providerName string) ProcessResult {
	file := domain.File{ID: fileID, LibraryName: lib.Name, Path: videoPath, ContentHash: contentHash}
	return p.processFile(ctx, lib, file, videoPath, lang, force, providerName)
}

// processFile handles a single (video, language) pair: gate check, search,
// score, download, sync, write sidecar, strip, and record outcome. file
// must already be registered (see registerFile) with a valid ID and
// Content Hash for videoPath. providerName identifies which Provider is
// attempting the pair, used only for no-candidate miss recording.
func (p *Pipeline) processFile(
	ctx context.Context,
	lib domain.Library,
	file domain.File,
	videoPath string,
	lang language.Tag,
	force bool,
	providerName string,
) ProcessResult {
	hash := media.ContentHash(file.ContentHash)

	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	if !force {
		needsFetch, err := strip.NeedsFetch(dir, stem, lang, hash)
		if err != nil {
			p.logger().Error("checking marker gate failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
			return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("checking marker gate: %w", err)}
		}
		if !needsFetch {
			if err := p.Store.MarkSynced(ctx, file.ID, lang); err != nil {
				p.logger().Error("marking already-synced file failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
				return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("marking already-synced file: %w", err)}
			}
			p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusPending, domain.StatusSynced, domain.FailureNone, nil)
			return ProcessResult{Outcome: OutcomeSkipped}
		}
	}

	if err := p.Store.MarkInProgress(ctx, file.ID, lang); err != nil {
		if errors.Is(err, store.ErrClaimLost) {
			return ProcessResult{Outcome: OutcomeSkipped}
		}
		p.logger().Error("marking in progress failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("marking in progress: %w", err)}
	}
	p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusPending, domain.StatusInProgress, domain.FailureNone, nil)

	query, queryErr := queryFromPath(videoPath, lang)
	if queryErr != nil {
		p.logger().Warn("filename metadata unparseable; falling back to hash-only search",
			"library", lib.Name, "path", videoPath, "language", lang.String(), "error", queryErr)
	}

	candidates, err := p.Provider.Search(ctx, query)
	if err != nil {
		var quotaErr *provider.QuotaExhaustedError
		if errors.As(err, &quotaErr) {
			if pendingErr := p.markPending(ctx, lib, videoPath, lang, file.ID); pendingErr != nil {
				return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, pendingErr)}
			}
			return ProcessResult{Outcome: OutcomePending}
		}
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureRetrievalFailed, err); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("searching provider: %w", err)}
	}
	p.logger().Debug("provider search", "provider", providerName, "library", lib.Name, "path", videoPath, "language", lang.String(), "candidates", len(candidates))

	info, parseErr := scoring.Parse(filepath.Base(videoPath))
	if parseErr != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, parseErr); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(parseErr, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("parsing video filename: %w", parseErr)}
	}

	best, ok := scoring.Select(info, candidates)
	if !ok {
		if topMiss, score, cutoff, found := scoring.Best(info, candidates); found {
			p.logger().Debug("scoring miss", "provider", providerName, "library", lib.Name, "path", videoPath, "language", lang.String(),
				"top_title", topMiss.Title, "top_year", topMiss.Year, "top_season", topMiss.Season, "top_episode", topMiss.Episode,
				"score", score, "cutoff", cutoff)
		}
		if err := p.Store.RecordProviderMiss(ctx, file.ID, lang, providerName); err != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("recording provider miss: %w", err)}
		}
		p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusInProgress, domain.StatusPending, domain.FailureNone, nil)
		return ProcessResult{Outcome: OutcomeNoCandidateMiss}
	}

	subtitleContent, err := p.Provider.Download(ctx, best)
	if err != nil {
		var quotaErr *provider.QuotaExhaustedError
		if errors.As(err, &quotaErr) {
			if pendingErr := p.markPending(ctx, lib, videoPath, lang, file.ID); pendingErr != nil {
				return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, pendingErr)}
			}
			return ProcessResult{Outcome: OutcomePending}
		}
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureRetrievalFailed, err); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("downloading candidate: %w", err)}
	}

	syncedContent, err := p.syncSubtitle(ctx, videoPath, subtitleContent)
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureSyncFailed, err); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("syncing subtitle: %w", err)}
	}

	sidecarExt := ".srt"
	codec, ok := marker.CodecFor(sidecarExt)
	if !ok {
		noCodecErr := fmt.Errorf("no marker codec for %s", sidecarExt)
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, noCodecErr); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: markErr}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: noCodecErr}
	}

	removedStreams, err := p.Stripper.StripEmbedded(ctx, videoPath, lib.StripScope, lang)
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("stripping embedded streams: %w", err)}
	}

	// StripEmbedded's ffmpeg remux changes the video's bytes when removing a
	// stream, so the Marker hash must reflect the settled post-Strip file.
	if len(removedStreams) > 0 {
		correctedHash, err := media.ComputeContentHash(videoPath)
		if err != nil {
			if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
				return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
			}
			return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("recomputing content hash after strip: %w", err)}
		}
		hash = correctedHash

		if err := p.Store.UpdateContentHash(ctx, file.ID, string(hash)); err != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("recording corrected content hash: %w", err)}
		}
	}

	markedContent, err := codec.Write(syncedContent, marker.Marker{ContentHash: hash})
	if err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("embedding marker: %w", err)}
	}

	if _, err := p.Stripper.Swap(ctx, videoPath, lang, lib.StripScope, hash, sidecarExt, markedContent); err != nil {
		if markErr := p.markFailed(ctx, lib, videoPath, lang, file.ID, domain.FailureInternalError, err); markErr != nil {
			return ProcessResult{Outcome: OutcomeFailed, Err: errors.Join(err, markErr)}
		}
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("swapping sidecar: %w", err)}
	}

	if err := p.Store.MarkSynced(ctx, file.ID, lang); err != nil {
		p.logger().Error("marking synced failed", "library", lib.Name, "path", videoPath, "language", lang.String(), "error", err)
		return ProcessResult{Outcome: OutcomeFailed, Err: fmt.Errorf("marking synced: %w", err)}
	}
	p.logStatusChange(ctx, lib, videoPath, lang, domain.StatusInProgress, domain.StatusSynced, domain.FailureNone, nil)

	return ProcessResult{Outcome: OutcomeSynced}
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

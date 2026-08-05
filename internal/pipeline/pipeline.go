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

// Stripper is the subset of strip.FFStripper the pipeline needs, extracted
// as an interface so tests can inject a fake that doesn't shell out to
// ffprobe/ffmpeg.
type Stripper interface {
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
// lib.Languages. It returns after all files have been processed (or ctx is
// cancelled).
func (p *Pipeline) Run(ctx context.Context, lib domain.Library, opts ...RunOption) (Result, error) {
	videos, err := p.scanLibrary(lib.Path)
	if err != nil {
		return Result{}, fmt.Errorf("pipeline: scanning library %q: %w", lib.Name, err)
	}
	return p.runFiles(ctx, lib, videos, opts...)
}

// RunFile processes a single video file within lib for each of
// lib.Languages, without scanning the rest of the Library. It's the
// entrypoint triggers use for fsnotify watch events and single-file manual
// reprocessing, where a full Library scan would be wasteful.
func (p *Pipeline) RunFile(ctx context.Context, lib domain.Library, videoPath string, opts ...RunOption) (Result, error) {
	return p.runFiles(ctx, lib, []string{videoPath}, opts...)
}

func (p *Pipeline) runFiles(ctx context.Context, lib domain.Library, videos []string, opts ...RunOption) (Result, error) {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	result := Result{
		FilesScanned: len(videos),
		Errors:       make(map[string]error),
	}

	if len(videos) == 0 {
		return result, nil
	}

	workerCount := p.WorkerCount
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}

	type job struct {
		videoPath string
		lang      language.Tag
	}

	jobs := make(chan job, len(videos)*len(lib.Languages))
	for _, videoPath := range videos {
		for _, lang := range lib.Languages {
			jobs <- job{videoPath: videoPath, lang: lang}
		}
	}
	close(jobs)

	type outcome struct {
		videoPath string
		lang      language.Tag
		outcome   processOutcome
		err       error
	}

	outcomes := make(chan outcome, cap(jobs))

	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				select {
				case <-ctx.Done():
					outcomes <- outcome{
						videoPath: j.videoPath,
						lang:      j.lang,
						outcome:   outcomeFailed,
						err:       ctx.Err(),
					}
					continue
				default:
				}

				res, syncErr := p.processFile(ctx, lib, j.videoPath, j.lang, cfg.force)
				outcomes <- outcome{
					videoPath: j.videoPath,
					lang:      j.lang,
					outcome:   res,
					err:       syncErr,
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(outcomes)
	}()

	for o := range outcomes {
		switch o.outcome {
		case outcomeFailed:
			result.Failed++
			if o.err != nil {
				result.Errors[fmt.Sprintf("%s:%s", o.videoPath, o.lang)] = o.err
			}
			p.logger().Error("subtitle sync failed",
				"library", lib.Name, "path", o.videoPath, "language", o.lang.String(), "error", o.err)
		case outcomeSkipped:
			result.Skipped++
		case outcomeSynced:
			result.Synced++
		case outcomeTerminal:
			result.NoCandidate++
			p.logger().Warn("no candidate cleared the scoring cutoff",
				"library", lib.Name, "path", o.videoPath, "language", o.lang.String())
		}
	}

	return result, nil
}

// IsVideoFile reports whether path has a file extension the pipeline
// recognizes as a video file.
func IsVideoFile(path string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(path))]
}

// scanLibrary walks lib.Path and returns the absolute paths of all video
// files found.
func (p *Pipeline) scanLibrary(root string) ([]string, error) {
	var videos []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if IsVideoFile(path) {
			videos = append(videos, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return videos, nil
}

// processFile handles a single (video, language) pair: hash, gate check,
// search, score, download, sync, write sidecar, strip, and record outcome.
func (p *Pipeline) processFile(
	ctx context.Context,
	lib domain.Library,
	videoPath string,
	lang language.Tag,
	force bool,
) (processOutcome, error) {
	hash, err := media.ComputeContentHash(videoPath)
	if err != nil {
		return outcomeFailed, fmt.Errorf("computing content hash: %w", err)
	}

	file, err := p.Store.ObserveFileContentHash(ctx, lib.Name, videoPath, string(hash))
	if err != nil {
		return outcomeFailed, fmt.Errorf("upserting file in store: %w", err)
	}

	if err := p.Store.EnsureLanguage(ctx, file.ID, lang); err != nil {
		return outcomeFailed, fmt.Errorf("ensuring language state: %w", err)
	}

	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	if !force {
		needsFetch, err := strip.NeedsFetch(dir, stem, lang, hash)
		if err != nil {
			return outcomeFailed, fmt.Errorf("checking marker gate: %w", err)
		}
		if !needsFetch {
			if err := p.Store.MarkSynced(ctx, file.ID, lang); err != nil {
				return outcomeFailed, fmt.Errorf("marking already-synced file: %w", err)
			}
			return outcomeSkipped, nil
		}
	}

	query, queryErr := queryFromPath(videoPath, lang)
	if queryErr != nil {
		p.logger().Warn("filename metadata unparseable; falling back to hash-only search",
			"library", lib.Name, "path", videoPath, "language", lang.String(), "error", queryErr)
	}

	candidates, err := p.Provider.Search(ctx, query)
	if err != nil {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureRetrievalFailed); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("searching provider: %w", err)
	}

	info, parseErr := scoring.Parse(filepath.Base(videoPath))
	if parseErr != nil {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureInternalError); markErr != nil {
			return outcomeFailed, errors.Join(parseErr, markErr)
		}
		return outcomeFailed, fmt.Errorf("parsing video filename: %w", parseErr)
	}

	best, ok := scoring.Select(info, candidates)
	if !ok {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureNoCandidate); markErr != nil {
			return outcomeFailed, markErr
		}
		return outcomeTerminal, nil
	}

	subtitleContent, err := p.Provider.Download(ctx, best)
	if err != nil {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureRetrievalFailed); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("downloading candidate: %w", err)
	}

	syncedContent, err := p.syncSubtitle(ctx, videoPath, subtitleContent)
	if err != nil {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureSyncFailed); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("syncing subtitle: %w", err)
	}

	sidecarExt := ".srt"
	codec, ok := marker.CodecFor(sidecarExt)
	if !ok {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureInternalError); markErr != nil {
			return outcomeFailed, markErr
		}
		return outcomeFailed, fmt.Errorf("no marker codec for %s", sidecarExt)
	}

	markedContent, err := codec.Write(syncedContent, marker.Marker{ContentHash: hash})
	if err != nil {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureInternalError); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("embedding marker: %w", err)
	}

	if _, err := p.Stripper.Swap(ctx, videoPath, lang, lib.StripScope, hash, sidecarExt, markedContent); err != nil {
		if markErr := p.Store.MarkFailed(ctx, file.ID, lang, domain.FailureInternalError); markErr != nil {
			return outcomeFailed, errors.Join(err, markErr)
		}
		return outcomeFailed, fmt.Errorf("swapping sidecar: %w", err)
	}

	if err := p.Store.MarkSynced(ctx, file.ID, lang); err != nil {
		return outcomeFailed, fmt.Errorf("marking synced: %w", err)
	}

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

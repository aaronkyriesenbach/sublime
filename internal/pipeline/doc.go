// Package pipeline orchestrates Sublime's end-to-end subtitle sync workflow:
// for each video file in a Library, compute its Content Hash, check whether
// a Marker-embedded sidecar already exists, and if not, search/score/select
// a Candidate from a Provider, Sync it via a Sync Engine, write a
// Marker-embedded sidecar, Strip old subtitles, and record the outcome in
// the state store.
//
// # Discovery
//
// A Library scan streams discoveries as its directory walk progresses,
// rather than walking the entire tree before anything is registered. Every
// file is classified the instant it's seen as Found (never tracked before)
// or Changed (already tracked, but its Content Hash differs or a manual
// reprocess targets it) — see CONTEXT.md — which fans a Pending Sync
// Status out for each of its Library's configured languages immediately,
// ahead of worker pickup. RunFile applies the same classification for a
// single file, used by fsnotify watch events and manual reprocessing.
//
// # Concurrency Model
//
// The pipeline processes files concurrently using a bounded worker pool.
// Provider operations (Search/Download) are assumed to have their own rate
// limiting (see internal/provider), so the pipeline does not limit those.
//
// CPU/IO-bound operations (ffprobe/ffmpeg for embedded stream handling,
// Sync Engine alignment, file hashing and I/O) share a single worker pool.
// The pool is sized to runtime.NumCPU() by default — both ffmpeg stream
// copying and alass audio analysis are CPU-intensive, and exceeding the CPU
// count just adds context-switching overhead. For mostly-I/O workloads
// (small files, fast disks), the OS scheduler handles I/O wait efficiently
// without needing a larger pool.
//
// The pool size is configurable via Pipeline.WorkerCount for environments
// where the default doesn't fit (e.g., constrained containers, or tests
// that want deterministic single-threaded execution).
package pipeline

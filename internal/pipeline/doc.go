// Package pipeline orchestrates Sublime's end-to-end subtitle sync workflow:
// for each video file in a Library, compute its Content Hash, check whether
// a Marker-embedded sidecar already exists, and if not, search/score/select
// a Candidate from a Provider, Sync it via a Sync Engine, write a
// Marker-embedded sidecar, Strip old subtitles, and record the outcome in
// the state store.
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

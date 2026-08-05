# Sublime

A self-hosted, containerized tool that watches media libraries, removes their existing subtitles, and retrieves/syncs high-quality replacement subtitles.

## Language

**Library**:
A single filesystem root path (plus its subdirectories) registered with the tool, containing one or more video files whose subtitles are managed. A tool instance can watch multiple independent Libraries, each with its own settings.
_Avoid_: Media folder, root folder, share

**Strip**:
Removing a video's existing subtitles that are not verifiably produced by Sublime for that exact file — both embedded/muxed subtitle streams and sidecar files. A subtitle Sublime previously produced for the current version of the file (per its Marker) is recognized and left in place rather than stripped.
_Avoid_: Clean, remove subtitles

**Marker**:
A machine-readable record embedded inside a Sublime-produced subtitle file (not in the filename) identifying that Sublime produced it and binding it to the Content Hash of the video it was synced against. Used to recognize already-synced subtitles after state loss, without trusting the DB as the sole source of truth.
_Avoid_: Tag, signature, fingerprint

**Content Hash**:
A fast, fixed-cost hash Sublime computes for a video file (independent of file size) used to detect whether a file's content has changed, and to bind a Marker to the exact file version it was synced against. Owned by Sublime, not any Provider — a Provider may independently derive whatever hash or query key it needs from the file for its own matching, which may or may not resemble the Content Hash. Captured only once Strip's embedded-stream removal has finished for that sync pass: Strip mutates the file, so a hash taken before it runs would never match the file's settled state, and a Marker or state-store record bound to that earlier hash would misread Sublime's own edit as an external content change on the next scan.
_Avoid_: Checksum, fingerprint, provider hash

**Candidate**:
A subtitle result returned by a Provider search, not yet chosen. Candidates are scored against the video's metadata (title, year, season/episode, source, release group, resolution, codec); the highest-scoring Candidate above the minimum cutoff is selected, or none is if nothing clears it.
_Avoid_: Result, match (as a noun — Match is reserved for a scored attribute)

**Provider**:
An external subtitle source Sublime can query for Candidates (e.g., OpenSubtitles). Each Provider owns its own rate limit and derives whatever hash or query key it needs from the video internally; Sublime supports one Provider in v1 but is built to support several.
_Avoid_: Source, backend

**Sync Engine**:
The tool Sublime uses to perform Sync (e.g., alass). Swappable independently of the Provider used to retrieve the Candidate.
_Avoid_: Aligner, sync tool

**Sync**:
Actively re-timing a retrieved subtitle's timestamps to align with a video's actual audio, using audio-based alignment — not merely placing a file next to the video. Always performed, even after a successful hash match, since a claimed match can still be mistimed.
_Avoid_: Match, align (as a standalone term — use Sync)

**Sync Status**:
The lifecycle state of a single (file, language) pair: Pending, In Progress, Synced, or Failed. Owned by the state store, not any Provider or Sync Engine.
_Avoid_: State, status (too generic on their own — always qualify as Sync Status)

**Found**:
The moment a video file is first recorded by the state store — via a Library scan's directory walk or a file-system watch event for a brand-new path — registering it and fanning out a Pending Sync Status for each of its Library's configured languages. Logged once per file, not per language.
_Avoid_: Discovered, scanned, indexed

**Changed**:
The moment an already-tracked file's Content Hash is observed to differ from its last-recorded value (an external edit, e.g. a re-encode), or a manual reprocess request targets it — resetting its existing Sync Statuses back to Pending in place. Distinct from Found: a Changed file was already known to Sublime.
_Avoid_: Modified, updated, rescanned

**Pending**:
The Sync Status of a (file, language) pair that has been Found (or reset by a Changed event) but not yet picked up by a worker. Counted for every tracked file regardless of how large the backlog is — not bounded by worker count.
_Avoid_: Queued, waiting, new

**In Progress**:
The Sync Status of a (file, language) pair currently checked out by a pipeline worker, from its Marker gate check through a final Synced/Failed outcome. Bounded by the pipeline's worker count — reflects the actual in-flight batch, not the backlog. A Marker-gate hit (already synced, no Provider work needed) skips In Progress entirely and goes straight from Pending to Synced, so In Progress only ever reflects real work. Deliberately not "Syncing" — that would overload Sync's specific re-timing meaning with a much broader in-flight-work meaning.
_Avoid_: Syncing, processing, active, working

**Synced** (Sync Status):
The Sync Status of a (file, language) pair whose subtitle is up to date with the file's current Content Hash, whether from a fresh Provider fetch this pass or a prior pass's still-valid Marker.
_Avoid_: Done, complete

**Failed** (Sync Status):
The Sync Status of a (file, language) pair whose most recent attempt did not produce a Synced subtitle, paired with a failure reason (no candidate cleared the scoring cutoff, retrieval failed, Sync failed, or an internal error). Not retried until a Changed event resets it.
_Avoid_: Error, broken

// Package trigger implements Sublime's Trigger model: how files enter the
// pipeline's Pending state in the first place (see internal/pipeline and
// internal/dispatcher). A Trigger only registers Found/Changed files and
// fans out Pending Sync Statuses for them — it never itself claims a
// (file, language) pair or calls a Provider; that's internal/dispatcher's
// job, running as an independent goroutine that coordinates with Trigger
// only through the state store. See
// docs/adr/0004-decouple-trigger-and-dispatcher.md.
//
// There are three entrypoints:
//
//   - Initial scan: Watcher.Start runs a full pipeline.Pipeline.Run over a
//     Library before it starts watching, registering every pre-existing
//     video file.
//   - Live watch: Watcher.Start then watches the Library's directory tree
//     with fsnotify and registers new/changed video files via
//     pipeline.Pipeline.RunFile as they appear. There is no periodic
//     rescanning — already-registered files are only revisited when
//     fsnotify reports a change, or via manual reprocessing.
//   - Manual reprocess: Reprocess resets a file, a directory, or an entire
//     Library's Sync Statuses to Pending regardless of prior state
//     (force-bypassing the Marker+Content-Hash gate once the Dispatcher
//     later claims them), for operator-triggered reprocessing.
package trigger

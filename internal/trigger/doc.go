// Package trigger implements Sublime's Trigger model: how files enter the
// pipeline (see internal/pipeline) in the first place.
//
// There are three entrypoints:
//
//   - Initial scan: Watcher.Start runs a full pipeline.Pipeline.Run over a
//     Library before it starts watching, so every pre-existing video file is
//     processed once on registration.
//   - Live watch: Watcher.Start then watches the Library's directory tree
//     with fsnotify and feeds new/changed video files through
//     pipeline.Pipeline.RunFile as they appear. There is no periodic
//     rescanning — already-processed files are only revisited when fsnotify
//     reports a change, or via manual reprocessing.
//   - Manual reprocess: Reprocess forces a file, a directory, or an entire
//     Library through the pipeline regardless of prior state (bypassing the
//     Marker+Content-Hash gate), for operator-triggered reprocessing.
package trigger

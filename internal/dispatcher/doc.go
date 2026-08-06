// Package dispatcher implements the Dispatcher half of Sublime's Trigger/
// Dispatcher split (see docs/adr/0004-decouple-trigger-and-dispatcher.md
// and CONTEXT.md's Dispatcher entry): a single, process-wide loop that
// claims Pending (file, language) pairs written by any Library's Trigger
// and hands them to the pipeline for the actual Search/Score/Download/
// Sync/Strip work, independently of whichever Trigger call registered
// them.
package dispatcher

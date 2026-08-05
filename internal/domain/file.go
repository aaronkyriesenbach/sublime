package domain

import "golang.org/x/text/language"

// SyncStatus is the state of a single (file, language) pair's subtitle sync,
// as tracked by the state store.
type SyncStatus string

const (
	// StatusPending means no sync has yet succeeded or failed for this
	// (file, language) pair, either because it's new or because a Content
	// Hash change reset it.
	StatusPending SyncStatus = "pending"

	// StatusInProgress means a pipeline worker currently has this
	// (file, language) pair checked out, from its Marker gate check
	// through a final StatusSynced/StatusFailed outcome. Deliberately not
	// "syncing" — see CONTEXT.md's In Progress entry for why that name
	// would collide with Sync's specific re-timing meaning.
	StatusInProgress SyncStatus = "in_progress"

	// StatusSynced means a subtitle was retrieved and Synced for the
	// file's current Content Hash.
	StatusSynced SyncStatus = "synced"

	// StatusFailed means the most recent attempt did not produce a Synced
	// subtitle. FailureReason records why.
	StatusFailed SyncStatus = "failed"
)

// FailureReason enumerates why a (file, language) pair is in StatusFailed.
// It is only meaningful when Status is StatusFailed.
type FailureReason string

const (
	// FailureNone indicates there is no failure recorded — the state is
	// not StatusFailed.
	FailureNone FailureReason = ""

	// FailureNoCandidate means no Candidate cleared the minimum score
	// cutoff.
	FailureNoCandidate FailureReason = "no_candidate"

	// FailureRetrievalFailed means a Candidate was selected but couldn't
	// be retrieved from its Provider.
	FailureRetrievalFailed FailureReason = "retrieval_failed"

	// FailureSyncFailed means a retrieved subtitle could not be Synced by
	// the Sync Engine.
	FailureSyncFailed FailureReason = "sync_failed"

	// FailureInternalError means an unexpected internal error interrupted
	// the attempt, unrelated to the Candidate or Sync Engine.
	FailureInternalError FailureReason = "internal_error"
)

// FileLanguageState is one (file, language) pair's durable sync state, as
// recorded by the state store.
type FileLanguageState struct {
	// Language is the target language for this state, as a canonical IETF
	// BCP 47 tag.
	Language language.Tag

	// Status is this pair's current sync status.
	Status SyncStatus

	// FailureReason explains why Status is StatusFailed. It is
	// FailureNone whenever Status is not StatusFailed.
	FailureReason FailureReason
}

// File is a single tracked video file's durable state: its identity within a
// Library, its last-observed Content Hash, and its per-language sync states.
type File struct {
	// ID is the state store's identifier for this file.
	ID int64

	// LibraryName is the owning Library's Name, as configured. The state
	// store does not validate this against config — config is the source
	// of truth for which Libraries exist.
	LibraryName string

	// Path is the file's path, as configured relative to its Library
	// convention (the state store treats it as an opaque identifier).
	Path string

	// ContentHash is Sublime's Content Hash for this file as of the last
	// time it was recorded. A change to this value resets every one of
	// the file's language states in place.
	ContentHash string

	// Languages holds this file's per-language sync states.
	Languages []FileLanguageState
}

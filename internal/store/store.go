// Package store is Sublime's durable state store: a SQLite database
// tracking every tracked file's Content Hash and each of its (file,
// language) sync states, so the rest of the pipeline survives process
// restarts without re-deriving what's already been Synced.
//
// The store is bootstrap-only: Open creates the schema shown in
// bootstrapSQL if it doesn't already exist, and there is no migration
// framework. Schema changes across releases are out of scope for v1.
//
// A `libraries` table is deliberately absent — config.yaml is the source of
// truth for which Libraries exist and what their target languages are. The
// store only knows about files and their language states, keyed by the
// Library name a caller supplies.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers as "sqlite"
)

// ErrLanguageStateNotFound is returned by MarkSynced and MarkFailed when no
// (file, language) row exists to update. Callers must EnsureLanguage before
// recording an outcome for a language.
var ErrLanguageStateNotFound = errors.New("store: language state not found")

// ErrFileNotFound is returned by UpdateContentHash when no file row exists
// to update. Callers must have already recorded the file via
// ObserveFileContentHash.
var ErrFileNotFound = errors.New("store: file not found")

// ErrClaimLost is returned by MarkInProgress when the (file, language) row
// exists but is no longer StatusPending, whether because another caller
// already claimed it or for any other reason. It is not distinguished from
// ErrLanguageStateNotFound beyond that: nothing in the store deletes
// file_language_states rows, so a genuinely missing row is practically
// unreachable once a pair has been registered, and both cases warrant the
// same reaction from a caller (skip, don't treat as a processing failure).
var ErrClaimLost = errors.New("store: claim lost, language state is no longer pending")

// bootstrapSQL is Sublime's entire schema. There is no migration framework:
// every statement uses IF NOT EXISTS so Open is safe to call against an
// existing database file.
const bootstrapSQL = `
CREATE TABLE IF NOT EXISTS files (
	id INTEGER PRIMARY KEY,
	library_name TEXT NOT NULL,
	path TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (library_name, path)
);

CREATE TABLE IF NOT EXISTS file_language_states (
	id INTEGER PRIMARY KEY,
	file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
	language TEXT NOT NULL,
	-- See docs/adr/0002-no-migration-for-in-progress-status.md: widening
	-- this CHECK to add 'in_progress' is an accepted breaking change for
	-- existing databases, not migrated.
	status TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'synced', 'failed')),
	failure_reason TEXT CHECK (
		(status = 'failed' AND failure_reason IN ('no_candidate', 'retrieval_failed', 'sync_failed', 'internal_error'))
		OR (status != 'failed' AND failure_reason IS NULL)
	),
	updated_at TEXT NOT NULL,
	UNIQUE (file_id, language)
);

CREATE INDEX IF NOT EXISTS idx_file_language_states_file_id ON file_language_states(file_id);
`

// Store is a handle to Sublime's SQLite-backed state store.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and
// bootstraps its schema. Callers must Close the returned Store.
//
// SQLite serializes writers regardless of connection count, and the pure-Go
// driver's foreign key enforcement is a per-connection PRAGMA, so the pool
// is capped at a single connection to keep both consistent across every
// statement the Store runs.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_foreign_keys=on", path))
	if err != nil {
		return nil, fmt.Errorf("opening state store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(bootstrapSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrapping state store schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close releases the state store's underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// withTx runs fn inside a transaction, committing on success and rolling
// back on error or panic.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			return
		}
		err = tx.Commit()
	}()

	return fn(tx)
}

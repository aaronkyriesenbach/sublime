package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// FileHashObservation classifies what ObserveFileHash found when recording
// a file's Content Hash, distinguishing a brand-new path (Found) from an
// already-tracked one whose hash did or didn't change — see CONTEXT.md's
// Found and Changed entries. The pipeline combines this with its own
// force-reprocess flag to decide the Found-vs-Changed classification it
// logs; the store only knows about the hash itself.
type FileHashObservation int

const (
	// FileHashNew means path had never been recorded before this call; the
	// file row was just inserted, with no language rows yet.
	FileHashNew FileHashObservation = iota

	// FileHashChanged means path was already tracked but contentHash
	// differs from its last-recorded value; its existing language rows
	// were reset to StatusPending in place.
	FileHashChanged

	// FileHashUnchanged means path was already tracked and contentHash
	// matches its last-recorded value; nothing was changed.
	FileHashUnchanged
)

// ObserveFileContentHash records path's current Content Hash within
// libraryName, inserting a new file row if none exists yet.
//
// If a file row already exists and contentHash matches its stored value,
// its language states are left untouched. If contentHash differs, every one
// of the file's existing language rows is reset to StatusPending in place
// (FailureReason cleared, no new rows created) before the new hash is
// recorded. This reset-on-mismatch behavior represents a fresh scan
// observing a hash change and implying the file's content has changed;
// when Sublime needs to update its own content hash record without
// resetting language states, use UpdateContentHash instead.
func (s *Store) ObserveFileContentHash(ctx context.Context, libraryName, path, contentHash string) (domain.File, error) {
	file, _, err := s.ObserveFileHash(ctx, libraryName, path, contentHash)
	return file, err
}

// ObserveFileHash is ObserveFileContentHash plus a FileHashObservation
// reporting whether path was newly inserted, changed, or unchanged, so
// callers (the pipeline) can classify a file as Found or Changed without
// re-deriving that from the hash themselves.
func (s *Store) ObserveFileHash(ctx context.Context, libraryName, path, contentHash string) (domain.File, FileHashObservation, error) {
	now := nowString()
	var file domain.File
	observation := FileHashUnchanged

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var id int64
		var existingHash string
		err := tx.QueryRowContext(ctx,
			`SELECT id, content_hash FROM files WHERE library_name = ? AND path = ?`,
			libraryName, path,
		).Scan(&id, &existingHash)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx,
				`INSERT INTO files (library_name, path, content_hash, updated_at) VALUES (?, ?, ?, ?)`,
				libraryName, path, contentHash, now,
			)
			if err != nil {
				return fmt.Errorf("inserting file: %w", err)
			}
			id, err = res.LastInsertId()
			if err != nil {
				return fmt.Errorf("reading inserted file id: %w", err)
			}
			observation = FileHashNew

		case err != nil:
			return fmt.Errorf("looking up file: %w", err)

		case existingHash != contentHash:
			observation = FileHashChanged
			if _, err := tx.ExecContext(ctx,
				`UPDATE files SET content_hash = ?, updated_at = ? WHERE id = ?`,
				contentHash, now, id,
			); err != nil {
				return fmt.Errorf("updating file content hash: %w", err)
			}
			// TODO(race): this reset is unconditional on the target rows'
			// current status, so a Changed event for a file with a
			// language pair currently In Progress resets it to Pending out
			// from under the in-flight worker. That worker's eventual
			// MarkSynced/MarkFailed (also unconditional) can then stomp
			// this fresh Pending row with an outcome computed against the
			// stale, pre-change content. Pre-existing, not introduced by
			// Provider Suspension dispatch-gating — needs its own
			// investigation (e.g. fencing the terminal-status writes on
			// the Content Hash they were computed against).
			if _, err := tx.ExecContext(ctx,
				`UPDATE file_language_states SET status = ?, failure_reason = NULL, force = 0, attempted_providers = '', updated_at = ? WHERE file_id = ?`,
				domain.StatusPending, now, id,
			); err != nil {
				return fmt.Errorf("resetting language states after content hash change: %w", err)
			}
		}

		languages, err := queryLanguageStates(ctx, tx, id)
		if err != nil {
			return err
		}

		file = domain.File{
			ID:          id,
			LibraryName: libraryName,
			Path:        path,
			ContentHash: contentHash,
			Languages:   languages,
		}
		return nil
	})
	if err != nil {
		return domain.File{}, FileHashUnchanged, err
	}

	return file, observation, nil
}

// ResetToPending resets fileID's existing language rows to StatusPending in
// place (FailureReason cleared, updated_at bumped), without touching its
// Content Hash or inserting any new rows. Unlike ObserveFileHash's
// reset-on-hash-change, this is for a manual reprocess request against an
// already-tracked file whose Content Hash hasn't necessarily changed — a
// Changed event per CONTEXT.md even though the hash itself may be identical.
func (s *Store) ResetToPending(ctx context.Context, fileID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = NULL, force = 0, attempted_providers = '', updated_at = ? WHERE file_id = ?`,
		domain.StatusPending, nowString(), fileID,
	)
	if err != nil {
		return fmt.Errorf("resetting language states to pending: %w", err)
	}
	return nil
}

// ResetToPendingForced is ResetToPending plus setting force = 1 on every one
// of fileID's language rows, marking them so a future Dispatcher claim
// bypasses the Marker+Content-Hash gate for this pass — see
// docs/adr/0004-decouple-trigger-and-dispatcher.md. Used for a manual
// reprocess request: unlike ResetToPending's plain reset, the whole point
// of a forced reprocess is to resync even when a still-valid Marker would
// otherwise make the gate skip straight to StatusSynced. The flag is
// cleared by MarkInProgress once the Dispatcher claims the row.
func (s *Store) ResetToPendingForced(ctx context.Context, fileID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = NULL, force = 1, attempted_providers = '', updated_at = ? WHERE file_id = ?`,
		domain.StatusPending, nowString(), fileID,
	)
	if err != nil {
		return fmt.Errorf("resetting language states to pending (forced): %w", err)
	}
	return nil
}

// ResetLanguageToPending resets fileID's single (file, language) row to
// StatusPending in place (FailureReason cleared, updated_at bumped),
// without touching any of the file's other language rows or its Content
// Hash. Unlike ResetToPending, which resets every language row for a file,
// this is scoped to one language — a quota-exhausted attempt on one
// language shouldn't reset the state of a file's other, unrelated
// languages. It returns ErrLanguageStateNotFound if no such row exists;
// callers must EnsureLanguage first.
func (s *Store) ResetLanguageToPending(ctx context.Context, fileID int64, lang language.Tag) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = NULL, updated_at = ? WHERE file_id = ? AND language = ?`,
		domain.StatusPending, nowString(), fileID, lang.String(),
	)
	if err != nil {
		return fmt.Errorf("resetting language state to pending: %w", err)
	}
	return checkUpdated(res)
}

// RecordProviderMiss records that providerName searched this (file,
// language) pair's current cycle and found no Candidate clearing the
// scoring cutoff: it appends providerName to the pair's attempted-Provider
// set (a no-op if already present) and resets the row to StatusPending with
// no FailureReason, mirroring how a QuotaExhaustedError already avoids
// marking Failed (see ResetLanguageToPending) rather than landing on Failed
// immediately — see docs/adr/0008-tiered-provider-chain.md. It returns
// ErrLanguageStateNotFound if no such row exists; callers must
// EnsureLanguage first.
func (s *Store) RecordProviderMiss(ctx context.Context, fileID int64, lang language.Tag, providerName string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var attempted string
		err := tx.QueryRowContext(ctx,
			`SELECT attempted_providers FROM file_language_states WHERE file_id = ? AND language = ?`,
			fileID, lang.String(),
		).Scan(&attempted)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLanguageStateNotFound
		}
		if err != nil {
			return fmt.Errorf("reading attempted providers: %w", err)
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE file_language_states SET status = ?, failure_reason = NULL, attempted_providers = ?, updated_at = ? WHERE file_id = ? AND language = ?`,
			domain.StatusPending, addAttemptedProvider(attempted, providerName), nowString(), fileID, lang.String(),
		)
		if err != nil {
			return fmt.Errorf("recording provider miss: %w", err)
		}
		return checkUpdated(res)
	})
}

// AttemptedProviders returns the set of Provider names already recorded as
// tried-and-missed for this (file, language) pair's current cycle (see
// RecordProviderMiss), in the order they were recorded. It returns
// ErrLanguageStateNotFound if no such row exists.
func (s *Store) AttemptedProviders(ctx context.Context, fileID int64, lang language.Tag) ([]string, error) {
	var attempted string
	err := s.db.QueryRowContext(ctx,
		`SELECT attempted_providers FROM file_language_states WHERE file_id = ? AND language = ?`,
		fileID, lang.String(),
	).Scan(&attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLanguageStateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading attempted providers: %w", err)
	}
	return splitAttemptedProviders(attempted), nil
}

// splitAttemptedProviders parses attempted_providers' stored comma-separated
// representation, returning nil for an empty set.
func splitAttemptedProviders(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// addAttemptedProvider appends name to raw's comma-separated representation,
// returning raw unchanged if name is already present.
func addAttemptedProvider(raw, name string) string {
	for _, existing := range splitAttemptedProviders(raw) {
		if existing == name {
			return raw
		}
	}
	if raw == "" {
		return name
	}
	return raw + "," + name
}

// UpdateContentHash records a corrected Content Hash for an already-known
// fileID without resetting any of its language states, unlike
// ObserveFileContentHash. Use this when Sublime's own Strip pass mutated
// the video (changing its Content Hash) rather than an external content
// change — the two intents are distinguished by which method is called,
// not by inspecting the hash delta at each call site. Returns
// ErrFileNotFound if fileID doesn't exist.
func (s *Store) UpdateContentHash(ctx context.Context, fileID int64, contentHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE files SET content_hash = ?, updated_at = ? WHERE id = ?`,
		contentHash, nowString(), fileID,
	)
	if err != nil {
		return fmt.Errorf("updating content hash: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected: %w", err)
	}
	if n == 0 {
		return ErrFileNotFound
	}
	return nil
}

// FilePathsForLibrary returns every tracked path within libraryName that is pathPrefix or a descendant of it — a lean alternative to ListFiles for reconciliation's disk-vs-store diff, scoped to whatever subtree was actually walked (a Reprocess target may be a Library subdirectory, not the whole Library).
func (s *Store) FilePathsForLibrary(ctx context.Context, libraryName, pathPrefix string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM files WHERE library_name = ? AND (path = ? OR path LIKE ? ESCAPE '\')`,
		libraryName, pathPrefix, escapeLike(pathPrefix)+string(filepath.Separator)+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("querying file paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scanning file path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating file paths: %w", err)
	}
	return paths, nil
}

// DeleteFile removes libraryName's file row at path, cascading to its language states; an already-gone path is a silent no-op (false, nil), not an error.
func (s *Store) DeleteFile(ctx context.Context, libraryName, path string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM files WHERE library_name = ? AND path = ?`,
		libraryName, path,
	)
	if err != nil {
		return false, fmt.Errorf("deleting file: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected: %w", err)
	}
	return n > 0, nil
}

// EnsureLanguage guarantees fileID has a row for lang, inserting one as
// StatusPending if it doesn't already exist. It never modifies an existing
// row.
func (s *Store) EnsureLanguage(ctx context.Context, fileID int64, lang language.Tag) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO file_language_states (file_id, language, status, failure_reason, updated_at)
		 VALUES (?, ?, ?, NULL, ?)
		 ON CONFLICT (file_id, language) DO NOTHING`,
		fileID, lang.String(), domain.StatusPending, nowString(),
	)
	if err != nil {
		return fmt.Errorf("ensuring language state: %w", err)
	}
	return nil
}

// GetFile reads a file and its language states by Library name and path.
// The second return value is false if no such file has been recorded.
func (s *Store) GetFile(ctx context.Context, libraryName, path string) (domain.File, bool, error) {
	var id int64
	var contentHash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, content_hash FROM files WHERE library_name = ? AND path = ?`,
		libraryName, path,
	).Scan(&id, &contentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.File{}, false, nil
	}
	if err != nil {
		return domain.File{}, false, fmt.Errorf("looking up file: %w", err)
	}

	languages, err := queryLanguageStates(ctx, s.db, id)
	if err != nil {
		return domain.File{}, false, err
	}

	return domain.File{
		ID:          id,
		LibraryName: libraryName,
		Path:        path,
		ContentHash: contentHash,
		Languages:   languages,
	}, true, nil
}

// MarkSynced sets fileID's language state to StatusSynced, clearing any
// FailureReason. It returns ErrLanguageStateNotFound if no such row exists;
// callers must EnsureLanguage first.
func (s *Store) MarkSynced(ctx context.Context, fileID int64, lang language.Tag) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = NULL, updated_at = ? WHERE file_id = ? AND language = ?`,
		domain.StatusSynced, nowString(), fileID, lang.String(),
	)
	if err != nil {
		return fmt.Errorf("marking language state synced: %w", err)
	}
	return checkUpdated(res)
}

// MarkInProgress sets fileID's language state to StatusInProgress, clearing
// any FailureReason, but only if the row is currently StatusPending. It
// returns ErrClaimLost if the row exists but isn't StatusPending anymore —
// whether because another caller already claimed it or for any other
// reason; see ErrClaimLost's doc comment for why that's not distinguished
// from a genuinely missing row. Callers must EnsureLanguage first.
func (s *Store) MarkInProgress(ctx context.Context, fileID int64, lang language.Tag) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = NULL, force = 0, updated_at = ? WHERE file_id = ? AND language = ? AND status = ?`,
		domain.StatusInProgress, nowString(), fileID, lang.String(), domain.StatusPending,
	)
	if err != nil {
		return fmt.Errorf("marking language state in progress: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected: %w", err)
	}
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// MarkFailed sets fileID's language state to StatusFailed with reason. It
// returns ErrLanguageStateNotFound if no such row exists; callers must
// EnsureLanguage first.
func (s *Store) MarkFailed(ctx context.Context, fileID int64, lang language.Tag, reason domain.FailureReason) error {
	if !validFailureReason(reason) {
		return fmt.Errorf("invalid failure reason %q", reason)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = ?, updated_at = ? WHERE file_id = ? AND language = ?`,
		domain.StatusFailed, reason, nowString(), fileID, lang.String(),
	)
	if err != nil {
		return fmt.Errorf("marking language state failed: %w", err)
	}
	return checkUpdated(res)
}

func validFailureReason(reason domain.FailureReason) bool {
	switch reason {
	case domain.FailureNoCandidate, domain.FailureRetrievalFailed, domain.FailureSyncFailed, domain.FailureInternalError:
		return true
	default:
		return false
	}
}

func checkUpdated(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected: %w", err)
	}
	if n == 0 {
		return ErrLanguageStateNotFound
	}
	return nil
}

// querier is satisfied by both *sql.DB and *sql.Tx, letting
// queryLanguageStates run inside or outside a transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryLanguageStates(ctx context.Context, q querier, fileID int64) ([]domain.FileLanguageState, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT language, status, COALESCE(failure_reason, ''), attempted_providers FROM file_language_states WHERE file_id = ? ORDER BY language`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying language states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var states []domain.FileLanguageState
	for rows.Next() {
		var langTag, status, failureReason, attempted string
		if err := rows.Scan(&langTag, &status, &failureReason, &attempted); err != nil {
			return nil, fmt.Errorf("scanning language state: %w", err)
		}

		parsed, err := language.Parse(langTag)
		if err != nil {
			return nil, fmt.Errorf("parsing stored language tag %q: %w", langTag, err)
		}

		states = append(states, domain.FileLanguageState{
			Language:      parsed,
			Status:        domain.SyncStatus(status),
			FailureReason: domain.FailureReason(failureReason),
			Attempted:     splitAttemptedProviders(attempted),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating language states: %w", err)
	}

	return states, nil
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

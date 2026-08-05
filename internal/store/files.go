package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
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
// resetting language states, use UpdateContentHash instead (once available).
func (s *Store) ObserveFileContentHash(ctx context.Context, libraryName, path, contentHash string) (domain.File, error) {
	now := nowString()
	var file domain.File

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

		case err != nil:
			return fmt.Errorf("looking up file: %w", err)

		case existingHash != contentHash:
			if _, err := tx.ExecContext(ctx,
				`UPDATE files SET content_hash = ?, updated_at = ? WHERE id = ?`,
				contentHash, now, id,
			); err != nil {
				return fmt.Errorf("updating file content hash: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE file_language_states SET status = ?, failure_reason = NULL, updated_at = ? WHERE file_id = ?`,
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
		return domain.File{}, err
	}

	return file, nil
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
// any FailureReason. It returns ErrLanguageStateNotFound if no such row
// exists; callers must EnsureLanguage first.
func (s *Store) MarkInProgress(ctx context.Context, fileID int64, lang language.Tag) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE file_language_states SET status = ?, failure_reason = NULL, updated_at = ? WHERE file_id = ? AND language = ?`,
		domain.StatusInProgress, nowString(), fileID, lang.String(),
	)
	if err != nil {
		return fmt.Errorf("marking language state in progress: %w", err)
	}
	return checkUpdated(res)
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
		`SELECT language, status, COALESCE(failure_reason, '') FROM file_language_states WHERE file_id = ? ORDER BY language`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying language states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var states []domain.FileLanguageState
	for rows.Next() {
		var langTag, status, failureReason string
		if err := rows.Scan(&langTag, &status, &failureReason); err != nil {
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

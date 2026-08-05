package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// LibrarySummary is one Library's aggregate (file, language) status counts,
// as recorded by the state store. Config, not the store, is the source of
// truth for which Libraries exist — a Library with no processed files yet
// has no LibrarySummary here at all; callers combine this with config to
// zero-fill.
type LibrarySummary struct {
	LibraryName string
	Pending     int
	InProgress  int
	Synced      int
	Failed      int
}

// LibrarySummaries returns per-Library counts of (file, language) states,
// one entry per Library name that has at least one tracked file. Libraries
// with no tracked files yet are absent, not zero-valued — see
// LibrarySummary's doc comment.
func (s *Store) LibrarySummaries(ctx context.Context) ([]LibrarySummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.library_name, fls.status, COUNT(*)
		FROM files f
		JOIN file_language_states fls ON fls.file_id = f.id
		GROUP BY f.library_name, fls.status
		ORDER BY f.library_name
	`)
	if err != nil {
		return nil, fmt.Errorf("querying library summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byName := make(map[string]*LibrarySummary)
	var order []string

	for rows.Next() {
		var libraryName, status string
		var count int
		if err := rows.Scan(&libraryName, &status, &count); err != nil {
			return nil, fmt.Errorf("scanning library summary row: %w", err)
		}

		sum, ok := byName[libraryName]
		if !ok {
			sum = &LibrarySummary{LibraryName: libraryName}
			byName[libraryName] = sum
			order = append(order, libraryName)
		}

		switch domain.SyncStatus(status) {
		case domain.StatusPending:
			sum.Pending = count
		case domain.StatusInProgress:
			sum.InProgress = count
		case domain.StatusSynced:
			sum.Synced = count
		case domain.StatusFailed:
			sum.Failed = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating library summaries: %w", err)
	}

	summaries := make([]LibrarySummary, 0, len(order))
	for _, name := range order {
		summaries = append(summaries, *byName[name])
	}
	return summaries, nil
}

// FileFilter scopes a ListFiles query.
type FileFilter struct {
	// LibraryName, if set, restricts results to that Library exactly.
	LibraryName string

	// PathPrefix, if set, restricts results to the file at that exact path,
	// or any file under it as a directory prefix.
	PathPrefix string

	// IncompleteOnly, if true, restricts results to files with at least
	// one language state that is not StatusSynced (StatusPending,
	// StatusInProgress, or StatusFailed) — a file whose every language is
	// StatusSynced is excluded. This is the default status filter the
	// HTTP API applies to /status.
	IncompleteOnly bool

	// Limit is the maximum number of files to return. Zero returns none;
	// callers must supply a positive value to get results.
	Limit int

	// Offset is the number of matching files (ordered by path) to skip
	// before collecting Limit results.
	Offset int
}

// ListFiles returns the files matching filter, ordered by path, along with
// the total number of matching files ignoring Limit/Offset (for pagination).
func (s *Store) ListFiles(ctx context.Context, filter FileFilter) ([]domain.File, int, error) {
	where := []string{"1 = 1"}
	args := []any{}

	if filter.LibraryName != "" {
		where = append(where, "library_name = ?")
		args = append(args, filter.LibraryName)
	}
	if filter.PathPrefix != "" {
		where = append(where, "(path = ? OR path LIKE ? ESCAPE '\\')")
		args = append(args, filter.PathPrefix, escapeLike(filter.PathPrefix)+string(filepath.Separator)+"%")
	}
	if filter.IncompleteOnly {
		where = append(where, `id IN (
			SELECT file_id FROM file_language_states WHERE status IN (?, ?, ?)
		)`)
		args = append(args, string(domain.StatusPending), string(domain.StatusInProgress), string(domain.StatusFailed))
	}

	whereClause := strings.Join(where, " AND ")

	var total int
	countRow := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM files WHERE "+whereClause, args...)
	if err := countRow.Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting matching files: %w", err)
	}

	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, library_name, path, content_hash FROM files WHERE "+whereClause+" ORDER BY path LIMIT ? OFFSET ?",
		pageArgs...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("querying files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		id                      int64
		libraryName, path, hash string
	}
	var matched []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.libraryName, &r.path, &r.hash); err != nil {
			return nil, 0, fmt.Errorf("scanning file row: %w", err)
		}
		matched = append(matched, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating files: %w", err)
	}

	files := make([]domain.File, 0, len(matched))
	for _, r := range matched {
		languages, err := queryLanguageStates(ctx, s.db, r.id)
		if err != nil {
			return nil, 0, err
		}
		files = append(files, domain.File{
			ID:          r.id,
			LibraryName: r.libraryName,
			Path:        r.path,
			ContentHash: r.hash,
			Languages:   languages,
		})
	}

	return files, total, nil
}

// escapeLike escapes SQLite LIKE metacharacters (%, _) in s so it can be
// used as a literal prefix with a caller-supplied ESCAPE '\' clause.
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

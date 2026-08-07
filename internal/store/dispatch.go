package store

import (
	"context"
	"fmt"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// PendingPair is one (file, language) row currently in StatusPending,
// joined with enough of its owning file's data for a Dispatcher to process
// it without a second round-trip.
type PendingPair struct {
	// FileID identifies the file within the state store.
	FileID int64

	// LibraryName is the owning Library's configured name.
	LibraryName string

	// Path is the file's path.
	Path string

	// ContentHash is the file's last-recorded Content Hash.
	ContentHash string

	// Language is this pair's target language.
	Language language.Tag

	// Force means this pair was reset to Pending by a manual reprocess
	// request (see ResetToPendingForced) and its Marker+Content-Hash gate
	// must be bypassed for this attempt.
	Force bool
}

// PendingPairs returns every (file, language) pair currently in
// StatusPending across every Library the store knows about, in a
// deterministic order (by file id, then language) so a Dispatcher's core
// pass is reproducible given the same store state.
func (s *Store) PendingPairs(ctx context.Context) ([]PendingPair, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.library_name, f.path, f.content_hash, fls.language, fls.force
		FROM file_language_states fls
		JOIN files f ON f.id = fls.file_id
		WHERE fls.status = ?
		ORDER BY f.id, fls.language
	`, string(domain.StatusPending))
	if err != nil {
		return nil, fmt.Errorf("querying pending pairs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var pairs []PendingPair
	for rows.Next() {
		var p PendingPair
		var langTag string
		var force int
		if err := rows.Scan(&p.FileID, &p.LibraryName, &p.Path, &p.ContentHash, &langTag, &force); err != nil {
			return nil, fmt.Errorf("scanning pending pair: %w", err)
		}
		parsed, err := language.Parse(langTag)
		if err != nil {
			return nil, fmt.Errorf("parsing stored language tag %q: %w", langTag, err)
		}
		p.Language = parsed
		p.Force = force != 0
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending pairs: %w", err)
	}

	return pairs, nil
}

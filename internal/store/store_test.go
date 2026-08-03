package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sublime.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	})
	return s
}

func mustLang(t *testing.T, tag string) language.Tag {
	t.Helper()
	parsed, err := language.Parse(tag)
	if err != nil {
		t.Fatalf("parsing language tag %q: %v", tag, err)
	}
	return parsed
}

func TestOpen_BootstrapsSchemaIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sublime.db")

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open returned error: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("closing first Store: %v", err)
	}

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open (against an existing file) returned error: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("closing second Store: %v", err)
	}
}

func TestUpsertFile_InsertsNewFileWithNoLanguages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}

	if file.ID == 0 {
		t.Errorf("expected a non-zero file ID, got %d", file.ID)
	}
	if file.LibraryName != "movies" || file.Path != "/media/movies/a.mkv" {
		t.Errorf("unexpected file identity: %+v", file)
	}
	if file.ContentHash != "hash-1" {
		t.Errorf("expected content hash %q, got %q", "hash-1", file.ContentHash)
	}
	if len(file.Languages) != 0 {
		t.Errorf("expected no language states on a newly inserted file, got %+v", file.Languages)
	}
}

func TestUpsertFile_SameHashLeavesLanguageStatesUntouched(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("initial UpsertFile returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}

	again, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("repeat UpsertFile returned error: %v", err)
	}

	if again.ID != file.ID {
		t.Fatalf("expected the same file ID on repeat upsert, got %d and %d", file.ID, again.ID)
	}
	if len(again.Languages) != 1 {
		t.Fatalf("expected 1 language state, got %d: %+v", len(again.Languages), again.Languages)
	}
	if got := again.Languages[0].Status; got != domain.StatusSynced {
		t.Errorf("expected status to remain %q after a same-hash upsert, got %q", domain.StatusSynced, got)
	}
}

func TestUpsertFile_HashChangeResetsLanguageStatesInPlace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")
	pt := mustLang(t, "pt-BR")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("initial UpsertFile returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage(en) returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, pt); err != nil {
		t.Fatalf("EnsureLanguage(pt-BR) returned error: %v", err)
	}
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}
	if err := s.MarkFailed(ctx, file.ID, pt, domain.FailureNoCandidate); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}

	changed, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-2")
	if err != nil {
		t.Fatalf("hash-change UpsertFile returned error: %v", err)
	}

	if changed.ID != file.ID {
		t.Fatalf("expected the same file ID after a hash change (reset in place, not a new row), got %d and %d", file.ID, changed.ID)
	}
	if changed.ContentHash != "hash-2" {
		t.Errorf("expected updated content hash %q, got %q", "hash-2", changed.ContentHash)
	}
	if len(changed.Languages) != 2 {
		t.Fatalf("expected the same 2 language rows to survive the reset, got %d: %+v", len(changed.Languages), changed.Languages)
	}
	for _, ls := range changed.Languages {
		if ls.Status != domain.StatusPending {
			t.Errorf("expected language %q to be reset to %q, got %q", ls.Language, domain.StatusPending, ls.Status)
		}
		if ls.FailureReason != domain.FailureNone {
			t.Errorf("expected language %q to have no failure reason after reset, got %q", ls.Language, ls.FailureReason)
		}
	}

	// The reset must not have created new rows alongside the old ones.
	fetched, ok, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetFile to find the file")
	}
	if len(fetched.Languages) != 2 {
		t.Fatalf("expected exactly 2 language rows after reset, got %d: %+v", len(fetched.Languages), fetched.Languages)
	}
}

func TestGetFile_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, ok, err := s.GetFile(ctx, "movies", "/media/movies/missing.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if ok {
		t.Fatal("expected GetFile to report not found for an unknown file")
	}
}

func TestEnsureLanguage_IsIdempotentAndDefaultsToPending(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
			t.Fatalf("EnsureLanguage call %d returned error: %v", i, err)
		}
	}

	got, ok, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetFile to find the file")
	}
	if len(got.Languages) != 1 {
		t.Fatalf("expected exactly 1 language row after repeated EnsureLanguage calls, got %d: %+v", len(got.Languages), got.Languages)
	}
	if got.Languages[0].Status != domain.StatusPending {
		t.Errorf("expected a newly ensured language to be %q, got %q", domain.StatusPending, got.Languages[0].Status)
	}
}

func TestEnsureLanguage_DoesNotOverwriteExistingStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}

	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("second EnsureLanguage returned error: %v", err)
	}

	got, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if got.Languages[0].Status != domain.StatusSynced {
		t.Errorf("expected EnsureLanguage to leave an existing status untouched, got %q", got.Languages[0].Status)
	}
}

func TestStatusTransitions_PendingToSyncedToFailedToPending(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	assertState := func(wantStatus domain.SyncStatus, wantReason domain.FailureReason) {
		t.Helper()
		got, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
		if err != nil {
			t.Fatalf("GetFile returned error: %v", err)
		}
		if len(got.Languages) != 1 {
			t.Fatalf("expected exactly 1 language row, got %d", len(got.Languages))
		}
		if got.Languages[0].Status != wantStatus {
			t.Errorf("expected status %q, got %q", wantStatus, got.Languages[0].Status)
		}
		if got.Languages[0].FailureReason != wantReason {
			t.Errorf("expected failure reason %q, got %q", wantReason, got.Languages[0].FailureReason)
		}
	}

	// pending (initial state from EnsureLanguage)
	assertState(domain.StatusPending, domain.FailureNone)

	// pending -> synced
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}
	assertState(domain.StatusSynced, domain.FailureNone)

	// synced -> failed
	if err := s.MarkFailed(ctx, file.ID, en, domain.FailureSyncFailed); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}
	assertState(domain.StatusFailed, domain.FailureSyncFailed)

	// failed -> synced (a retry outside this store succeeded)
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("second MarkSynced returned error: %v", err)
	}
	assertState(domain.StatusSynced, domain.FailureNone)

	// synced -> pending (only reachable via a content hash change)
	if _, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-2"); err != nil {
		t.Fatalf("hash-change UpsertFile returned error: %v", err)
	}
	assertState(domain.StatusPending, domain.FailureNone)
}

func TestMarkSynced_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}

	err = s.MarkSynced(ctx, file.ID, en)
	if !errors.Is(err, store.ErrLanguageStateNotFound) {
		t.Fatalf("expected ErrLanguageStateNotFound, got %v", err)
	}
}

func TestMarkFailed_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}

	err = s.MarkFailed(ctx, file.ID, en, domain.FailureNoCandidate)
	if !errors.Is(err, store.ErrLanguageStateNotFound) {
		t.Fatalf("expected ErrLanguageStateNotFound, got %v", err)
	}
}

func TestMarkFailed_RejectsInvalidReason(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.UpsertFile(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	if err := s.MarkFailed(ctx, file.ID, en, domain.FailureReason("not_a_real_reason")); err == nil {
		t.Fatal("expected an error for an unrecognized failure reason, got nil")
	}
	if err := s.MarkFailed(ctx, file.ID, en, domain.FailureNone); err == nil {
		t.Fatal("expected an error when passing FailureNone to MarkFailed, got nil")
	}
}

func TestDifferentLibrariesWithSamePathAreDistinctFiles(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	movies, err := s.UpsertFile(ctx, "movies", "/shared/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile (movies) returned error: %v", err)
	}
	tv, err := s.UpsertFile(ctx, "tv", "/shared/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("UpsertFile (tv) returned error: %v", err)
	}

	if movies.ID == tv.ID {
		t.Errorf("expected distinct file IDs for the same path under different libraries, got %d for both", movies.ID)
	}
}

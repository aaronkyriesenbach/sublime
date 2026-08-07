package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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

func TestObserveFileContentHash_InsertsNewFileWithNoLanguages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

func TestObserveFileContentHash_SameHashLeavesLanguageStatesUntouched(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("initial ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}

	again, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("repeat ObserveFileContentHash returned error: %v", err)
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

func TestObserveFileContentHash_HashChangeResetsLanguageStatesInPlace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")
	pt := mustLang(t, "pt-BR")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("initial ObserveFileContentHash returned error: %v", err)
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

	changed, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-2")
	if err != nil {
		t.Fatalf("hash-change ObserveFileContentHash returned error: %v", err)
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

func TestObserveFileHash_ReportsFileHashNewOnFirstInsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	file, obs, err := s.ObserveFileHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileHash returned error: %v", err)
	}

	if obs != store.FileHashNew {
		t.Errorf("observation = %v, want FileHashNew", obs)
	}
	if file.ID == 0 {
		t.Errorf("expected a non-zero file ID, got %d", file.ID)
	}
}

func TestObserveFileHash_ReportsFileHashUnchangedOnSameHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.ObserveFileHash(ctx, "movies", "/media/movies/a.mkv", "hash-1"); err != nil {
		t.Fatalf("initial ObserveFileHash returned error: %v", err)
	}

	_, obs, err := s.ObserveFileHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("repeat ObserveFileHash returned error: %v", err)
	}

	if obs != store.FileHashUnchanged {
		t.Errorf("observation = %v, want FileHashUnchanged", obs)
	}
}

func TestObserveFileHash_ReportsFileHashChangedOnDifferentHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.ObserveFileHash(ctx, "movies", "/media/movies/a.mkv", "hash-1"); err != nil {
		t.Fatalf("initial ObserveFileHash returned error: %v", err)
	}

	_, obs, err := s.ObserveFileHash(ctx, "movies", "/media/movies/a.mkv", "hash-2")
	if err != nil {
		t.Fatalf("hash-change ObserveFileHash returned error: %v", err)
	}

	if obs != store.FileHashChanged {
		t.Errorf("observation = %v, want FileHashChanged", obs)
	}
}

func TestResetToPending_ResetsExistingLanguageStatesRegardlessOfContentHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")
	pt := mustLang(t, "pt-BR")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

	// A manual reprocess request resets an already-tracked file's language
	// states to Pending even though its Content Hash hasn't changed.
	if err := s.ResetToPending(ctx, file.ID); err != nil {
		t.Fatalf("ResetToPending returned error: %v", err)
	}

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
	for _, ls := range fetched.Languages {
		if ls.Status != domain.StatusPending {
			t.Errorf("expected language %q to be reset to %q, got %q", ls.Language, domain.StatusPending, ls.Status)
		}
		if ls.FailureReason != domain.FailureNone {
			t.Errorf("expected language %q to have no failure reason after reset, got %q", ls.Language, ls.FailureReason)
		}
	}
}

func TestResetLanguageToPending_ResetsOnlyTheGivenLanguage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")
	pt := mustLang(t, "pt-BR")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage(en) returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, pt); err != nil {
		t.Fatalf("EnsureLanguage(pt-BR) returned error: %v", err)
	}
	if err := s.MarkFailed(ctx, file.ID, en, domain.FailureRetrievalFailed); err != nil {
		t.Fatalf("MarkFailed(en) returned error: %v", err)
	}
	if err := s.MarkSynced(ctx, file.ID, pt); err != nil {
		t.Fatalf("MarkSynced(pt-BR) returned error: %v", err)
	}

	// A quota-exhausted attempt on one language shouldn't touch the state
	// of the file's other, unrelated languages.
	if err := s.ResetLanguageToPending(ctx, file.ID, en); err != nil {
		t.Fatalf("ResetLanguageToPending returned error: %v", err)
	}

	fetched, ok, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetFile to find the file")
	}
	if len(fetched.Languages) != 2 {
		t.Fatalf("expected exactly 2 language rows, got %d: %+v", len(fetched.Languages), fetched.Languages)
	}
	for _, ls := range fetched.Languages {
		switch ls.Language {
		case en:
			if ls.Status != domain.StatusPending {
				t.Errorf("expected en to be reset to %q, got %q", domain.StatusPending, ls.Status)
			}
			if ls.FailureReason != domain.FailureNone {
				t.Errorf("expected en to have no failure reason after reset, got %q", ls.FailureReason)
			}
		case pt:
			if ls.Status != domain.StatusSynced {
				t.Errorf("expected pt-BR to remain untouched at %q, got %q", domain.StatusSynced, ls.Status)
			}
		default:
			t.Errorf("unexpected language state: %+v", ls)
		}
	}
}

func TestResetLanguageToPending_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}

	err = s.ResetLanguageToPending(ctx, file.ID, en)
	if !errors.Is(err, store.ErrLanguageStateNotFound) {
		t.Fatalf("expected ErrLanguageStateNotFound, got %v", err)
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

func TestUpdateContentHash_LeavesLanguageStatesUntouched(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}

	if err := s.UpdateContentHash(ctx, file.ID, "hash-corrected"); err != nil {
		t.Fatalf("UpdateContentHash returned error: %v", err)
	}

	updated, ok, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetFile to find the file")
	}
	if updated.ContentHash != "hash-corrected" {
		t.Errorf("expected content hash %q, got %q", "hash-corrected", updated.ContentHash)
	}
	if len(updated.Languages) != 1 {
		t.Fatalf("expected 1 language state, got %d: %+v", len(updated.Languages), updated.Languages)
	}
	if updated.Languages[0].Status != domain.StatusSynced {
		t.Errorf("expected status to remain %q, got %q", domain.StatusSynced, updated.Languages[0].Status)
	}
}

func TestUpdateContentHash_UnknownFile(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	err := s.UpdateContentHash(ctx, 12345, "hash-1")
	if !errors.Is(err, store.ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}
}

func TestEnsureLanguage_IsIdempotentAndDefaultsToPending(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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
	if _, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-2"); err != nil {
		t.Fatalf("hash-change ObserveFileContentHash returned error: %v", err)
	}
	assertState(domain.StatusPending, domain.FailureNone)
}

func TestMarkSynced_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
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

func TestMarkInProgress_TransitionsFromPendingAndBackToSyncedOrFailed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	if err := s.MarkInProgress(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkInProgress returned error: %v", err)
	}

	got, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if got.Languages[0].Status != domain.StatusInProgress {
		t.Errorf("expected status %q, got %q", domain.StatusInProgress, got.Languages[0].Status)
	}
	if got.Languages[0].FailureReason != domain.FailureNone {
		t.Errorf("expected no failure reason while in progress, got %q", got.Languages[0].FailureReason)
	}

	// in_progress -> synced
	if err := s.MarkSynced(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkSynced returned error: %v", err)
	}
	got, _, err = s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if got.Languages[0].Status != domain.StatusSynced {
		t.Errorf("expected status %q, got %q", domain.StatusSynced, got.Languages[0].Status)
	}

	// back to pending, then in_progress, then to failed
	if err := s.ResetLanguageToPending(ctx, file.ID, en); err != nil {
		t.Fatalf("ResetLanguageToPending returned error: %v", err)
	}
	if err := s.MarkInProgress(ctx, file.ID, en); err != nil {
		t.Fatalf("second MarkInProgress returned error: %v", err)
	}
	if err := s.MarkFailed(ctx, file.ID, en, domain.FailureRetrievalFailed); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}
	got, _, err = s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if got.Languages[0].Status != domain.StatusFailed || got.Languages[0].FailureReason != domain.FailureRetrievalFailed {
		t.Errorf("expected failed/retrieval_failed, got %q/%q", got.Languages[0].Status, got.Languages[0].FailureReason)
	}
}

func TestMarkInProgress_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}

	// No row exists at all for this (file, language) pair, which surfaces
	// the same way as a row that's no longer pending: nothing in the store
	// deletes file_language_states rows, so callers can't tell these apart
	// and shouldn't need to.
	err = s.MarkInProgress(ctx, file.ID, en)
	if !errors.Is(err, store.ErrClaimLost) {
		t.Fatalf("expected ErrClaimLost, got %v", err)
	}
}

func TestMarkInProgress_ClaimLostWhenNotPending(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	// First claim succeeds from pending.
	if err := s.MarkInProgress(ctx, file.ID, en); err != nil {
		t.Fatalf("first MarkInProgress returned error: %v", err)
	}

	// A second claim attempt against the same (now in_progress) row must
	// lose, leaving the first claim's state untouched.
	err = s.MarkInProgress(ctx, file.ID, en)
	if !errors.Is(err, store.ErrClaimLost) {
		t.Fatalf("expected ErrClaimLost, got %v", err)
	}

	got, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if got.Languages[0].Status != domain.StatusInProgress {
		t.Errorf("expected status to remain %q, got %q", domain.StatusInProgress, got.Languages[0].Status)
	}
}

func TestLibrarySummaries_CountsStatusesPerLibrary(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")
	es := mustLang(t, "es")

	movieA, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, movieA.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkSynced(ctx, movieA.ID, en); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	movieB, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/b.mkv", "hash-2")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, movieB.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkFailed(ctx, movieB.ID, en, domain.FailureNoCandidate); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	movieC, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/c.mkv", "hash-4")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, movieC.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkInProgress(ctx, movieC.ID, en); err != nil {
		t.Fatalf("MarkInProgress: %v", err)
	}

	tvA, err := s.ObserveFileContentHash(ctx, "tv", "/media/tv/a.mkv", "hash-3")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, tvA.ID, es); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	// Left pending.

	summaries, err := s.LibrarySummaries(ctx)
	if err != nil {
		t.Fatalf("LibrarySummaries: %v", err)
	}

	byName := map[string]store.LibrarySummary{}
	for _, sum := range summaries {
		byName[sum.LibraryName] = sum
	}

	movies, ok := byName["movies"]
	if !ok {
		t.Fatal("expected a summary for \"movies\"")
	}
	if movies.Synced != 1 || movies.Failed != 1 || movies.Pending != 0 || movies.InProgress != 1 {
		t.Errorf("movies summary = %+v, want Synced=1 Failed=1 Pending=0 InProgress=1", movies)
	}

	tv, ok := byName["tv"]
	if !ok {
		t.Fatal("expected a summary for \"tv\"")
	}
	if tv.Pending != 1 || tv.Synced != 0 || tv.Failed != 0 {
		t.Errorf("tv summary = %+v, want Pending=1 Synced=0 Failed=0", tv)
	}
}

func TestLibrarySummaries_NoFilesYieldsNoEntries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	summaries, err := s.LibrarySummaries(ctx)
	if err != nil {
		t.Fatalf("LibrarySummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected no summaries for an empty store, got %+v", summaries)
	}
}

func TestListFiles_DefaultFilterOmitsFullySyncedFiles(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	synced, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/synced.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, synced.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkSynced(ctx, synced.ID, en); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	failed, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/failed.mkv", "hash-2")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, failed.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkFailed(ctx, failed.ID, en, domain.FailureRetrievalFailed); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	pending, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/pending.mkv", "hash-3")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, pending.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	inProgress, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/inprogress.mkv", "hash-4")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, inProgress.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkInProgress(ctx, inProgress.ID, en); err != nil {
		t.Fatalf("MarkInProgress: %v", err)
	}

	files, total, err := s.ListFiles(ctx, store.FileFilter{
		LibraryName:    "movies",
		IncompleteOnly: true,
		Limit:          100,
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (synced file excluded by default)", total)
	}
	var gotPaths []string
	for _, f := range files {
		gotPaths = append(gotPaths, f.Path)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3: %v", len(files), gotPaths)
	}
	for _, want := range []string{failed.Path, pending.Path, inProgress.Path} {
		found := false
		for _, p := range gotPaths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in results, got %v", want, gotPaths)
		}
	}
}

func TestListFiles_StateAllIncludesSyncedFiles(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	synced, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/synced.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, synced.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}
	if err := s.MarkSynced(ctx, synced.ID, en); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	files, total, err := s.ListFiles(ctx, store.FileFilter{
		LibraryName: "movies",
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if total != 1 || len(files) != 1 {
		t.Fatalf("total=%d len(files)=%d, want 1 and 1", total, len(files))
	}
	if files[0].Path != synced.Path {
		t.Errorf("path = %q, want %q", files[0].Path, synced.Path)
	}
}

func TestListFiles_ScopesByLibraryName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	movieFile, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, movieFile.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	tvFile, err := s.ObserveFileContentHash(ctx, "tv", "/media/tv/a.mkv", "hash-2")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, tvFile.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	files, total, err := s.ListFiles(ctx, store.FileFilter{
		LibraryName: "movies",
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(files) != 1 || files[0].LibraryName != "movies" {
		t.Fatalf("files = %+v, want a single movies-library file", files)
	}
}

func TestListFiles_ScopesByPathPrefix(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	inScope, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/sub/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, inScope.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	outOfScope, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/other/b.mkv", "hash-2")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, outOfScope.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	files, total, err := s.ListFiles(ctx, store.FileFilter{
		PathPrefix: "/media/movies/sub",
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if total != 1 || len(files) != 1 {
		t.Fatalf("total=%d len(files)=%d, want 1 and 1", total, len(files))
	}
	if files[0].Path != inScope.Path {
		t.Errorf("path = %q, want %q", files[0].Path, inScope.Path)
	}
}

func TestListFiles_ExactPathMatchesAFileNotJustADirectoryPrefix(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	target, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, target.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	files, total, err := s.ListFiles(ctx, store.FileFilter{
		PathPrefix: target.Path,
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if total != 1 || len(files) != 1 {
		t.Fatalf("total=%d len(files)=%d, want 1 and 1", total, len(files))
	}
}

func TestListFiles_PaginatesWithLimitAndOffset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	paths := []string{
		"/media/movies/a.mkv",
		"/media/movies/b.mkv",
		"/media/movies/c.mkv",
	}
	for i, p := range paths {
		f, err := s.ObserveFileContentHash(ctx, "movies", p, fmt.Sprintf("hash-%d", i))
		if err != nil {
			t.Fatalf("ObserveFileContentHash: %v", err)
		}
		if err := s.EnsureLanguage(ctx, f.ID, en); err != nil {
			t.Fatalf("EnsureLanguage: %v", err)
		}
	}

	page1, total, err := s.ListFiles(ctx, store.FileFilter{LibraryName: "movies", Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("ListFiles page 1: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 = %d, want 2", len(page1))
	}

	page2, total2, err := s.ListFiles(ctx, store.FileFilter{LibraryName: "movies", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListFiles page 2: %v", err)
	}
	if total2 != 3 {
		t.Errorf("total2 = %d, want 3", total2)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 = %d, want 1", len(page2))
	}

	if page1[0].Path == page2[0].Path {
		t.Error("expected page1 and page2 to return different files")
	}
}

func TestRecordProviderMiss_AppendsNameAndResetsToPendingNoFailureReason(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.MarkInProgress(ctx, file.ID, en); err != nil {
		t.Fatalf("MarkInProgress returned error: %v", err)
	}

	// A no-candidate miss against a named Provider mirrors
	// QuotaExhaustedError's existing handling: reset to pending, no failure
	// reason recorded, per docs/adr/0008-tiered-provider-chain.md.
	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("RecordProviderMiss returned error: %v", err)
	}

	got, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if got.Languages[0].Status != domain.StatusPending {
		t.Errorf("expected status %q, got %q", domain.StatusPending, got.Languages[0].Status)
	}
	if got.Languages[0].FailureReason != domain.FailureNone {
		t.Errorf("expected no failure reason recorded, got %q", got.Languages[0].FailureReason)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 1 || attempted[0] != "opensubtitles" {
		t.Errorf("attempted = %v, want [opensubtitles]", attempted)
	}
}

func TestGetFile_SurfacesAttemptedProvidersOnLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	// Before any Provider has missed, GetFile must report no attempted
	// Providers rather than omitting the field silently.
	fresh, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	if len(fresh.Languages[0].Attempted) != 0 {
		t.Errorf("Attempted = %v, want empty before any RecordProviderMiss", fresh.Languages[0].Attempted)
	}

	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("first RecordProviderMiss returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "subdl"); err != nil {
		t.Fatalf("second RecordProviderMiss returned error: %v", err)
	}

	got, _, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("GetFile returned error: %v", err)
	}
	want := []string{"opensubtitles", "subdl"}
	if !slices.Equal(got.Languages[0].Attempted, want) {
		t.Errorf("Languages[0].Attempted = %v, want %v", got.Languages[0].Attempted, want)
	}
}

func TestListFiles_SurfacesAttemptedProvidersOnLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("RecordProviderMiss returned error: %v", err)
	}

	files, _, err := s.ListFiles(ctx, store.FileFilter{LibraryName: "movies", Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	want := []string{"opensubtitles"}
	if !slices.Equal(files[0].Languages[0].Attempted, want) {
		t.Errorf("Languages[0].Attempted = %v, want %v", files[0].Languages[0].Attempted, want)
	}
}

func TestRecordProviderMiss_AccumulatesMultipleDistinctProviders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("first RecordProviderMiss returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "subdl"); err != nil {
		t.Fatalf("second RecordProviderMiss returned error: %v", err)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 2 || attempted[0] != "opensubtitles" || attempted[1] != "subdl" {
		t.Errorf("attempted = %v, want [opensubtitles subdl]", attempted)
	}
}

func TestRecordProviderMiss_SameProviderTwiceDoesNotDuplicate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("first RecordProviderMiss returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("second RecordProviderMiss returned error: %v", err)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 1 || attempted[0] != "opensubtitles" {
		t.Errorf("attempted = %v, want [opensubtitles] (no duplicate)", attempted)
	}
}

func TestRecordProviderMiss_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}

	err = s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles")
	if !errors.Is(err, store.ErrLanguageStateNotFound) {
		t.Fatalf("expected ErrLanguageStateNotFound, got %v", err)
	}
}

func TestAttemptedProviders_EmptyForFreshLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 0 {
		t.Errorf("attempted = %v, want none", attempted)
	}
}

func TestAttemptedProviders_UnknownLanguageState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}

	_, err = s.AttemptedProviders(ctx, file.ID, en)
	if !errors.Is(err, store.ErrLanguageStateNotFound) {
		t.Fatalf("expected ErrLanguageStateNotFound, got %v", err)
	}
}

func TestObserveFileContentHash_HashChangeClearsAttemptedProviders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("RecordProviderMiss returned error: %v", err)
	}

	// A Changed event (Content Hash mismatch) starts a fresh cycle: the
	// attempted-Provider set from the prior cycle must not survive it.
	if _, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-2"); err != nil {
		t.Fatalf("hash-change ObserveFileContentHash returned error: %v", err)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 0 {
		t.Errorf("attempted = %v, want none after a Changed reset", attempted)
	}
}

func TestResetToPending_ClearsAttemptedProviders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("RecordProviderMiss returned error: %v", err)
	}

	// A manual reprocess request starts a fresh cycle too.
	if err := s.ResetToPending(ctx, file.ID); err != nil {
		t.Fatalf("ResetToPending returned error: %v", err)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 0 {
		t.Errorf("attempted = %v, want none after a manual reprocess reset", attempted)
	}
}

func TestResetToPendingForced_ClearsAttemptedProviders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash returned error: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage returned error: %v", err)
	}
	if err := s.RecordProviderMiss(ctx, file.ID, en, "opensubtitles"); err != nil {
		t.Fatalf("RecordProviderMiss returned error: %v", err)
	}

	// A forced manual reprocess request starts a fresh cycle too.
	if err := s.ResetToPendingForced(ctx, file.ID); err != nil {
		t.Fatalf("ResetToPendingForced returned error: %v", err)
	}

	attempted, err := s.AttemptedProviders(ctx, file.ID, en)
	if err != nil {
		t.Fatalf("AttemptedProviders returned error: %v", err)
	}
	if len(attempted) != 0 {
		t.Errorf("attempted = %v, want none after a forced reset", attempted)
	}
}

func TestDifferentLibrariesWithSamePathAreDistinctFiles(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	movies, err := s.ObserveFileContentHash(ctx, "movies", "/shared/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash (movies) returned error: %v", err)
	}
	tv, err := s.ObserveFileContentHash(ctx, "tv", "/shared/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash (tv) returned error: %v", err)
	}

	if movies.ID == tv.ID {
		t.Errorf("expected distinct file IDs for the same path under different libraries, got %d for both", movies.ID)
	}
}

func TestFilePathsForLibrary_ScopesByLibraryNameOnly(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if _, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/b.mkv", "hash-2"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if _, err := s.ObserveFileContentHash(ctx, "tv", "/media/tv/c.mkv", "hash-3"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}

	paths, err := s.FilePathsForLibrary(ctx, "movies", "/media/movies")
	if err != nil {
		t.Fatalf("FilePathsForLibrary: %v", err)
	}

	slices.Sort(paths)
	want := []string{"/media/movies/a.mkv", "/media/movies/b.mkv"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

// TestFilePathsForLibrary_ScopesByPathPrefix guards a Reprocess-scoped scan
// (whose Run target is a Library subdirectory, not the whole Library):
// reconciliation must never see a sibling file outside the walked subtree.
func TestFilePathsForLibrary_ScopesByPathPrefix(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.ObserveFileContentHash(ctx, "tv", "/media/tv/Show/S01/a.mkv", "hash-1"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if _, err := s.ObserveFileContentHash(ctx, "tv", "/media/tv/Show/S02/b.mkv", "hash-2"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}

	paths, err := s.FilePathsForLibrary(ctx, "tv", "/media/tv/Show/S01")
	if err != nil {
		t.Fatalf("FilePathsForLibrary: %v", err)
	}

	want := []string{"/media/tv/Show/S01/a.mkv"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v (S02's file must not leak into an S01-scoped diff)", paths, want)
	}
}

func TestFilePathsForLibrary_NoTrackedFilesReturnsEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	paths, err := s.FilePathsForLibrary(ctx, "movies", "/media/movies")
	if err != nil {
		t.Fatalf("FilePathsForLibrary: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want empty", paths)
	}
}

func TestDeleteFile_RemovesRowAndCascadesLanguageStates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	en := mustLang(t, "en")

	file, err := s.ObserveFileContentHash(ctx, "movies", "/media/movies/a.mkv", "hash-1")
	if err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if err := s.EnsureLanguage(ctx, file.ID, en); err != nil {
		t.Fatalf("EnsureLanguage: %v", err)
	}

	deleted, err := s.DeleteFile(ctx, "movies", "/media/movies/a.mkv")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if !deleted {
		t.Error("deleted = false, want true")
	}

	if _, found, err := s.GetFile(ctx, "movies", "/media/movies/a.mkv"); err != nil || found {
		t.Errorf("GetFile after DeleteFile: found=%v err=%v, want found=false", found, err)
	}

	// A second raw connection to the same file confirms ON DELETE CASCADE
	// actually fired, not just that files-first queries can't see the row.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening raw connection: %v", err)
	}
	defer func() { _ = raw.Close() }()

	var count int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_language_states WHERE file_id = ?`, file.ID).Scan(&count); err != nil {
		t.Fatalf("counting language states: %v", err)
	}
	if count != 0 {
		t.Errorf("file_language_states rows remaining for deleted file_id %d = %d, want 0", file.ID, count)
	}
}

func TestDeleteFile_AlreadyGoneIsSilentNoOp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	deleted, err := s.DeleteFile(ctx, "movies", "/media/movies/never-existed.mkv")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if deleted {
		t.Error("deleted = true, want false for a path that was never tracked")
	}
}

func TestDeleteFile_ScopesByLibraryName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.ObserveFileContentHash(ctx, "movies", "/shared/a.mkv", "hash-1"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}
	if _, err := s.ObserveFileContentHash(ctx, "tv", "/shared/a.mkv", "hash-1"); err != nil {
		t.Fatalf("ObserveFileContentHash: %v", err)
	}

	deleted, err := s.DeleteFile(ctx, "movies", "/shared/a.mkv")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if !deleted {
		t.Error("deleted = false, want true")
	}

	if _, found, err := s.GetFile(ctx, "tv", "/shared/a.mkv"); err != nil || !found {
		t.Errorf("tv library's file wrongly affected: found=%v err=%v, want found=true", found, err)
	}
}

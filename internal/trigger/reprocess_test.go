package trigger_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/dispatcher"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/store"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
	"github.com/aaronkyriesenbach/sublime/internal/trigger"
)

func writeSampleVideo(t *testing.T, dst string) {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "integration", "video", "sample.mp4")
	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading sample video: %v", err)
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		t.Fatalf("writing video %q: %v", dst, err)
	}
}

func newTestPipeline(t *testing.T, searched map[string]int) (*pipeline.Pipeline, *store.Store) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "sublime.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	candidateContent := "1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"
	fakeProvider := &provider.Fake{
		SearchFunc: func(ctx context.Context, q provider.Query) ([]domain.Candidate, error) {
			if searched != nil {
				searched[q.Path]++
			}
			return []domain.Candidate{{ID: "candidate", Title: q.Title, Year: q.Year}}, nil
		},
		DownloadFunc: func(ctx context.Context, c domain.Candidate) ([]byte, error) {
			return []byte(candidateContent), nil
		},
	}

	return &pipeline.Pipeline{
		Store:       st,
		Provider:    fakeProvider,
		SyncEngine:  &syncengine.FakeSyncEngine{},
		Stripper:    &pipeline.FakeStripper{},
		WorkerCount: 1,
	}, st
}

// dispatchOnce runs a single Dispatcher pass over every Pending pair
// currently in st for lib, standing in for the production Dispatcher
// goroutine (internal/dispatcher) these tests need to actually observe a
// Sync Status transition beyond Pending \u2014 Trigger's Run/RunFile/Reprocess
// no longer do that work themselves (see
// docs/adr/0004-decouple-trigger-and-dispatcher.md).
func dispatchOnce(t *testing.T, ctx context.Context, p *pipeline.Pipeline, st *store.Store, lib domain.Library) {
	t.Helper()
	d := &dispatcher.Dispatcher{Pipeline: p, Store: st, Libraries: []domain.Library{lib}}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("dispatcher.RunOnce: %v", err)
	}
}

func TestReprocess_SingleFileForcesResyncDespiteValidMarker(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeSampleVideo(t, videoPath)

	searched := map[string]int{}
	p, st := newTestPipeline(t, searched)

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("initial run: %v", err)
	}
	dispatchOnce(t, ctx, p, st, lib)
	if searched[videoPath] != 1 {
		t.Fatalf("searches after initial run = %d, want 1", searched[videoPath])
	}

	file, found, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected file to be Synced before reprocessing, got %+v", file)
	}

	if err := trigger.Reprocess(ctx, p, lib, videoPath); err != nil {
		t.Fatalf("Reprocess: %v", err)
	}

	// Reprocess is now a pure registration/reset operation: it must not
	// have searched again itself, only reset the pair to Pending (forced).
	if searched[videoPath] != 1 {
		t.Fatalf("searches immediately after Reprocess = %d, want still 1 (Reprocess doesn't dispatch)", searched[videoPath])
	}
	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != 1 || pairs[0].Path != videoPath || !pairs[0].Force {
		t.Fatalf("PendingPairs = %+v, want a single forced pending pair for %q", pairs, videoPath)
	}

	dispatchOnce(t, ctx, p, st, lib)
	if searched[videoPath] != 2 {
		t.Errorf("searches after Dispatcher claims the forced pair = %d, want 2 (gate must be bypassed)", searched[videoPath])
	}

	file, found, err = st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !found || file.Languages[0].Status != domain.StatusSynced {
		t.Fatalf("expected file to be Synced again after the forced dispatch, got %+v", file)
	}
}

func TestReprocess_DirectoryForcesOnlyFilesUnderIt(t *testing.T) {
	libDir := t.TempDir()
	subDir := filepath.Join(libDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	rootVideo := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	subVideo := filepath.Join(subDir, "Movie.Two.2021.HDTV.x264-GRP.mp4")
	writeSampleVideo(t, rootVideo)
	writeSampleVideo(t, subVideo)

	searched := map[string]int{}
	p, st := newTestPipeline(t, searched)

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("initial run: %v", err)
	}
	dispatchOnce(t, ctx, p, st, lib)
	searched[rootVideo] = 0
	searched[subVideo] = 0

	if err := trigger.Reprocess(ctx, p, lib, subDir); err != nil {
		t.Fatalf("Reprocess: %v", err)
	}

	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != 1 || pairs[0].Path != subVideo || !pairs[0].Force {
		t.Fatalf("PendingPairs = %+v, want a single forced pending pair for %q", pairs, subVideo)
	}

	dispatchOnce(t, ctx, p, st, lib)
	if searched[subVideo] != 1 {
		t.Errorf("searches for subVideo = %d, want 1", searched[subVideo])
	}
	if searched[rootVideo] != 0 {
		t.Errorf("searches for rootVideo = %d, want 0 (outside reprocess target)", searched[rootVideo])
	}
}

func TestReprocess_EntireLibraryForcesEveryFile(t *testing.T) {
	libDir := t.TempDir()
	video1 := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	video2 := filepath.Join(libDir, "Movie.Two.2021.HDTV.x264-GRP.mp4")
	writeSampleVideo(t, video1)
	writeSampleVideo(t, video2)

	searched := map[string]int{}
	p, st := newTestPipeline(t, searched)

	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("initial run: %v", err)
	}
	dispatchOnce(t, ctx, p, st, lib)
	searched[video1] = 0
	searched[video2] = 0

	if err := trigger.Reprocess(ctx, p, lib, ""); err != nil {
		t.Fatalf("Reprocess: %v", err)
	}

	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("PendingPairs = %+v, want 2 forced pending pairs", pairs)
	}
	for _, pair := range pairs {
		if !pair.Force {
			t.Errorf("pair %+v not marked Force", pair)
		}
	}

	dispatchOnce(t, ctx, p, st, lib)
	if searched[video1] != 1 || searched[video2] != 1 {
		t.Errorf("searches = %d,%d, want 1,1 (whole library reprocessed)", searched[video1], searched[video2])
	}
}

// TestReprocess_DirectorySkipsStripStrayTempFile is issue #81's regression test for the directory-walk entrypoint: a stray Strip temp file must be skipped, not reset to Pending.
func TestReprocess_DirectorySkipsStripStrayTempFile(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Movie.One.2020.HDTV.x264-GRP.mp4")
	writeSampleVideo(t, videoPath)

	strayPath := strip.NewTempVideoPath(libDir, "Movie.One.2020.HDTV.x264-GRP", ".mp4")
	writeSampleVideo(t, strayPath)

	p, st := newTestPipeline(t, nil)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}

	ctx := context.Background()
	if err := trigger.Reprocess(ctx, p, lib, libDir); err != nil {
		t.Fatalf("Reprocess: %v", err)
	}

	pairs, err := st.PendingPairs(ctx)
	if err != nil {
		t.Fatalf("PendingPairs: %v", err)
	}
	if len(pairs) != 1 || pairs[0].Path != videoPath {
		t.Fatalf("PendingPairs = %+v, want a single pending pair for %q (stray temp file must be skipped)", pairs, videoPath)
	}

	if _, found, err := st.GetFile(ctx, lib.Name, strayPath); err != nil || found {
		t.Errorf("stray temp file registered by directory reprocess: found=%v err=%v", found, err)
	}
}

func TestReprocess_NonVideoFileTargetErrors(t *testing.T) {
	libDir := t.TempDir()
	txtPath := filepath.Join(libDir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("not a video"), 0o644); err != nil {
		t.Fatalf("writing txt file: %v", err)
	}

	p, _ := newTestPipeline(t, nil)
	lib := domain.Library{
		Name:      "test-library",
		Path:      libDir,
		Languages: []language.Tag{language.English},
	}

	if err := trigger.Reprocess(context.Background(), p, lib, txtPath); err == nil {
		t.Error("Reprocess with non-video file target: want error, got nil")
	}
}

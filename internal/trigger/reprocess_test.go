package trigger_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/store"
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

func newTestPipeline(t *testing.T, searched map[string]int) *pipeline.Pipeline {
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
	}
}

func TestReprocess_SingleFileForcesResyncDespiteValidMarker(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	writeSampleVideo(t, videoPath)

	searched := map[string]int{}
	p := newTestPipeline(t, searched)

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
	if searched[videoPath] != 1 {
		t.Fatalf("searches after initial run = %d, want 1", searched[videoPath])
	}

	result, err := trigger.Reprocess(ctx, p, lib, videoPath)
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.Synced != 1 {
		t.Errorf("Synced = %d, want 1", result.Synced)
	}
	if searched[videoPath] != 2 {
		t.Errorf("searches after Reprocess = %d, want 2 (gate must be bypassed)", searched[videoPath])
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
	p := newTestPipeline(t, searched)

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
	searched[rootVideo] = 0
	searched[subVideo] = 0

	result, err := trigger.Reprocess(ctx, p, lib, subDir)
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.FilesScanned)
	}
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
	p := newTestPipeline(t, searched)

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
	searched[video1] = 0
	searched[video2] = 0

	result, err := trigger.Reprocess(ctx, p, lib, "")
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", result.FilesScanned)
	}
	if searched[video1] != 1 || searched[video2] != 1 {
		t.Errorf("searches = %d,%d, want 1,1 (whole library reprocessed)", searched[video1], searched[video2])
	}
}

func TestReprocess_NonVideoFileTargetErrors(t *testing.T) {
	libDir := t.TempDir()
	txtPath := filepath.Join(libDir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("not a video"), 0o644); err != nil {
		t.Fatalf("writing txt file: %v", err)
	}

	p := newTestPipeline(t, nil)
	lib := domain.Library{
		Name:      "test-library",
		Path:      libDir,
		Languages: []language.Tag{language.English},
	}

	if _, err := trigger.Reprocess(context.Background(), p, lib, txtPath); err == nil {
		t.Error("Reprocess with non-video file target: want error, got nil")
	}
}

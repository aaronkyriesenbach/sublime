package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
	"github.com/aaronkyriesenbach/sublime/internal/strip"
	"github.com/aaronkyriesenbach/sublime/internal/syncengine"
)

// swapHookStripper runs onSwap just before delegating Swap, letting a test
// inject an event after Strip has already changed the video.
type swapHookStripper struct {
	*pipeline.FakeStripper
	onSwap func()
}

func (s *swapHookStripper) Swap(
	ctx context.Context,
	videoPath string,
	lang language.Tag,
	scope domain.StripScope,
	hash media.ContentHash,
	sidecarExt string,
	sidecarContent []byte,
) (strip.SwapResult, error) {
	s.onSwap()
	return s.FakeStripper.Swap(ctx, videoPath, lang, scope, hash, sidecarExt, sidecarContent)
}

// TestPipeline_ChangedEventDuringProcessingIsNotOverwritten reproduces the
// race where a Changed event resets a pair while a worker is still In
// Progress: whatever outcome the worker reaches afterwards was computed
// against the old content and must be discarded, leaving the pair Pending.
func TestPipeline_ChangedEventDuringProcessingIsNotOverwritten(t *testing.T) {
	candidateContent := []byte("1\n00:00:00,500 --> 00:00:01,900\nOne two three\n")
	foundCandidate := func(context.Context, provider.Query) ([]domain.Candidate, error) {
		return []domain.Candidate{{ID: "test-candidate", Title: "Test Movie", Year: 2024}}, nil
	}

	tests := []struct {
		name string
		// provider builds the Provider; changed triggers the Changed event
		// at the point in the pair's processing the case wants to race.
		provider func(changed func()) *provider.Fake
		stripper func(changed func()) pipeline.Stripper
	}{
		{
			name: "miss recording",
			provider: func(changed func()) *provider.Fake {
				return &provider.Fake{SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
					changed()
					return nil, nil
				}}
			},
		},
		{
			name: "failed outcome",
			provider: func(changed func()) *provider.Fake {
				return &provider.Fake{
					SearchFunc: foundCandidate,
					DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
						changed()
						return nil, errors.New("download blew up")
					},
				}
			},
		},
		{
			name: "pending reset after quota exhaustion",
			provider: func(changed func()) *provider.Fake {
				return &provider.Fake{SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
					changed()
					return nil, &provider.QuotaExhaustedError{ResumeAt: time.Now().Add(time.Hour)}
				}}
			},
		},
		{
			name: "synced outcome",
			provider: func(changed func()) *provider.Fake {
				return &provider.Fake{
					SearchFunc: foundCandidate,
					DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
						changed()
						return candidateContent, nil
					},
				}
			},
		},
		{
			name: "synced outcome after the worker changed the hash itself via Strip",
			provider: func(changed func()) *provider.Fake {
				return &provider.Fake{
					SearchFunc: foundCandidate,
					DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
						return candidateContent, nil
					},
				}
			},
			stripper: func(changed func()) pipeline.Stripper {
				return &swapHookStripper{
					FakeStripper: &pipeline.FakeStripper{StripEmbeddedIndices: []int{2}},
					onSwap:       changed,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			libDir := t.TempDir()
			videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
			if err := os.WriteFile(videoPath, []byte("original video bytes"), 0o644); err != nil {
				t.Fatalf("writing video: %v", err)
			}

			lib := domain.Library{
				Name:       "test-library",
				Path:       libDir,
				Languages:  []language.Tag{language.English},
				StripScope: domain.StripScopeAll,
			}
			st := openTestStore(t)
			var logBuf bytes.Buffer

			var p *pipeline.Pipeline
			ctx := context.Background()
			changedHash := ""
			changed := func() {
				if err := os.WriteFile(videoPath, []byte("replaced video bytes, different length"), 0o644); err != nil {
					t.Errorf("replacing video: %v", err)
					return
				}
				hash, err := media.ComputeContentHash(videoPath)
				if err != nil {
					t.Errorf("hashing replaced video: %v", err)
					return
				}
				changedHash = string(hash)
				if _, err := p.RunFile(ctx, lib, videoPath); err != nil {
					t.Errorf("RunFile for Changed event: %v", err)
				}
			}

			var stripper pipeline.Stripper = &pipeline.FakeStripper{}
			if tt.stripper != nil {
				stripper = tt.stripper(changed)
			}
			p = &pipeline.Pipeline{
				Store:      st,
				Provider:   tt.provider(changed),
				SyncEngine: &syncengine.FakeSyncEngine{},
				Stripper:   stripper,
				Logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
			}

			if _, err := p.Run(ctx, lib); err != nil {
				t.Fatalf("Run: %v", err)
			}

			pairs, err := st.PendingPairs(ctx)
			if err != nil || len(pairs) != 1 {
				t.Fatalf("PendingPairs = %v, %v; want exactly 1 pair", pairs, err)
			}
			pair := pairs[0]
			result := p.ProcessPending(ctx, lib, pair.FileID, pair.ContentHash, pair.Path, pair.Language, pair.Force, "test-provider")

			if result.Err != nil {
				t.Errorf("ProcessPending Err = %v, want the stale outcome discarded without error", result.Err)
			}
			if result.Outcome != pipeline.OutcomeSkipped {
				t.Errorf("ProcessPending Outcome = %v, want OutcomeSkipped", result.Outcome)
			}

			file, _, err := st.GetFile(ctx, lib.Name, videoPath)
			if err != nil {
				t.Fatalf("GetFile: %v", err)
			}
			if file.ContentHash != changedHash {
				t.Errorf("store content hash = %q, want the Changed event's %q", file.ContentHash, changedHash)
			}
			state := file.Languages[0]
			if state.Status != domain.StatusPending || state.FailureReason != domain.FailureNone || len(state.Attempted) != 0 {
				t.Errorf("language state = %+v, want the fresh Pending row left intact", state)
			}

			logOutput := logBuf.String()
			for _, want := range []string{
				"level=WARN", "discarding stale result", "library=test-library", "path=" + videoPath, "language=en",
				"current_hash=" + changedHash,
			} {
				if !strings.Contains(logOutput, want) {
					t.Errorf("log output = %q, want it to contain %q", logOutput, want)
				}
			}
			if !strings.Contains(logOutput, "expected_hash=") || strings.Contains(logOutput, "expected_hash="+changedHash) {
				t.Errorf("log output = %q, want expected_hash to be the pre-Changed hash", logOutput)
			}
		})
	}
}

// TestPipeline_WorkerOwnPostStripHashDoesNotFenceItsOutcome guards that
// Strip changing the Content Hash mid-flight isn't mistaken for someone else
// changing it: the worker still records its Synced outcome.
func TestPipeline_WorkerOwnPostStripHashDoesNotFenceItsOutcome(t *testing.T) {
	libDir := t.TempDir()
	videoPath := filepath.Join(libDir, "Test.Movie.2024.HDTV.x264-FAKEGROUP.mp4")
	if err := os.WriteFile(videoPath, []byte("original video bytes"), 0o644); err != nil {
		t.Fatalf("writing video: %v", err)
	}

	st := openTestStore(t)
	lib := domain.Library{
		Name:       "test-library",
		Path:       libDir,
		Languages:  []language.Tag{language.English},
		StripScope: domain.StripScopeAll,
	}
	p := &pipeline.Pipeline{
		Store: st,
		Provider: &provider.Fake{
			SearchFunc: func(context.Context, provider.Query) ([]domain.Candidate, error) {
				return []domain.Candidate{{ID: "c", Title: "Test Movie", Year: 2024}}, nil
			},
			DownloadFunc: func(context.Context, domain.Candidate) ([]byte, error) {
				return []byte("1\n00:00:00,500 --> 00:00:01,900\nOne two three\n"), nil
			},
		},
		SyncEngine: &syncengine.FakeSyncEngine{},
		Stripper:   &pipeline.FakeStripper{StripEmbeddedIndices: []int{2}},
	}

	ctx := context.Background()
	if _, err := p.Run(ctx, lib); err != nil {
		t.Fatalf("Run: %v", err)
	}
	dispatchPending(t, ctx, p, st, lib)

	file, _, err := st.GetFile(ctx, lib.Name, videoPath)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if file.Languages[0].Status != domain.StatusSynced {
		t.Errorf("status = %q, want %q", file.Languages[0].Status, domain.StatusSynced)
	}
	postStrip, err := media.ComputeContentHash(videoPath)
	if err != nil {
		t.Fatalf("ComputeContentHash: %v", err)
	}
	if file.ContentHash != string(postStrip) {
		t.Errorf("store content hash = %q, want post-Strip %q", file.ContentHash, postStrip)
	}
}

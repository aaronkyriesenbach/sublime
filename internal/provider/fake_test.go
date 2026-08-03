package provider_test

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/provider"
)

func TestFake_Search_ReturnsConfiguredCandidates(t *testing.T) {
	want := []domain.Candidate{
		{ID: "sub-1", Title: "Arrival", Year: 2016, HashMatch: true},
		{ID: "sub-2", Title: "Arrival", Year: 2016, Source: "BluRay"},
	}

	var gotQuery provider.Query
	fake := &provider.Fake{
		SearchFunc: func(_ context.Context, q provider.Query) ([]domain.Candidate, error) {
			gotQuery = q
			return want, nil
		},
	}

	query := provider.Query{Title: "Arrival", Year: 2016, Language: language.English}
	got, err := fake.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d candidates, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if gotQuery != query {
		t.Errorf("SearchFunc received query %+v, want %+v", gotQuery, query)
	}
}

func TestFake_Search_ReturnsConfiguredError(t *testing.T) {
	wantErr := errors.New("provider unreachable")
	fake := &provider.Fake{
		SearchFunc: func(_ context.Context, _ provider.Query) ([]domain.Candidate, error) {
			return nil, wantErr
		},
	}

	_, err := fake.Search(context.Background(), provider.Query{Title: "Arrival"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected Search to return %v, got %v", wantErr, err)
	}
}

func TestFake_Search_NoFuncConfigured(t *testing.T) {
	fake := &provider.Fake{}

	got, err := fake.Search(context.Background(), provider.Query{Title: "Arrival"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no candidates, got %d", len(got))
	}
}

func TestFake_Download_ReturnsConfiguredBytes(t *testing.T) {
	want := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	candidate := domain.Candidate{ID: "sub-1", Title: "Arrival"}

	var gotCandidate domain.Candidate
	fake := &provider.Fake{
		DownloadFunc: func(_ context.Context, c domain.Candidate) ([]byte, error) {
			gotCandidate = c
			return want, nil
		},
	}

	got, err := fake.Download(context.Background(), candidate)
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Download() = %q, want %q", got, want)
	}
	if gotCandidate != candidate {
		t.Errorf("DownloadFunc received candidate %+v, want %+v", gotCandidate, candidate)
	}
}

func TestFake_Download_ReturnsConfiguredError(t *testing.T) {
	wantErr := errors.New("download quota exhausted")
	fake := &provider.Fake{
		DownloadFunc: func(_ context.Context, _ domain.Candidate) ([]byte, error) {
			return nil, wantErr
		},
	}

	_, err := fake.Download(context.Background(), domain.Candidate{ID: "sub-1"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected Download to return %v, got %v", wantErr, err)
	}
}

func TestFake_Download_NoFuncConfigured(t *testing.T) {
	fake := &provider.Fake{}

	_, err := fake.Download(context.Background(), domain.Candidate{ID: "sub-1"})
	if err == nil {
		t.Fatal("expected an error when no DownloadFunc is configured, got nil")
	}
}

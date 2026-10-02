package audiosource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
)

var _ audiosource.Source = (*audiosource.FakeSource)(nil)
var _ audiosource.Source = (*audiosource.FFSource)(nil)

func TestFakeSource_ReportsConfiguredStreams(t *testing.T) {
	fake := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1, Language: "eng"}}}

	streams, err := fake.AudioStreams(context.Background(), "/v.mkv")
	if err != nil || len(streams) != 1 || streams[0].Language != "eng" {
		t.Errorf("AudioStreams = %+v, %v", streams, err)
	}
}

func TestFakeSource_ExtractRecordsCallAndReturnsWAVOfRequestedLength(t *testing.T) {
	fake := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}}
	r := audiosource.Range{Start: time.Minute, Duration: 2 * time.Second}

	wav, err := fake.Extract(context.Background(), "/v.mkv", 1, r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDuration(t, wavPCMDuration(t, wav), 2*time.Second)

	want := audiosource.ExtractCall{VideoPath: "/v.mkv", StreamIndex: 1, Range: r}
	if len(fake.ExtractCalls) != 1 || fake.ExtractCalls[0] != want {
		t.Errorf("ExtractCalls = %+v, want [%+v]", fake.ExtractCalls, want)
	}
}

func TestFakeSource_ExtractUnknownStreamIsErrNoAudioStream(t *testing.T) {
	fake := &audiosource.FakeSource{}

	if _, err := fake.Extract(context.Background(), "/v.mkv", 3, audiosource.Range{}); !errors.Is(err, audiosource.ErrNoAudioStream) {
		t.Errorf("Extract error = %v, want ErrNoAudioStream", err)
	}
}

func TestFakeSource_ConfiguredErrorsAndAudioOverrideDefaults(t *testing.T) {
	boom := errors.New("boom")
	fake := &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, StreamsErr: boom, ExtractErr: boom}
	if _, err := fake.AudioStreams(context.Background(), "/v.mkv"); !errors.Is(err, boom) {
		t.Errorf("AudioStreams error = %v, want boom", err)
	}
	if _, err := fake.Extract(context.Background(), "/v.mkv", 1, audiosource.Range{}); !errors.Is(err, boom) {
		t.Errorf("Extract error = %v, want boom", err)
	}

	fake = &audiosource.FakeSource{Streams: []audiosource.Stream{{Index: 1}}, Audio: []byte("canned")}
	got, err := fake.Extract(context.Background(), "/v.mkv", 1, audiosource.Range{})
	if err != nil || string(got) != "canned" {
		t.Errorf("Extract = %q, %v; want canned audio", got, err)
	}
}

func TestFakeSource_ReportsConfiguredDuration(t *testing.T) {
	fake := &audiosource.FakeSource{VideoDuration: 2 * time.Hour}

	got, err := fake.Duration(context.Background(), "/v.mkv")
	if err != nil || got != 2*time.Hour {
		t.Errorf("Duration = %v, %v; want 2h", got, err)
	}
}

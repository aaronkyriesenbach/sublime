package audiosource_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/audiosource"
)

const fixtureVideo = "../../testdata/integration/video/sample.mp4"

// fixtureVideoDuration is the length of the integration fixture's audio.
const fixtureVideoDuration = 9 * time.Second

// durationTolerance absorbs AAC encoder priming and frame granularity.
const durationTolerance = 100 * time.Millisecond

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	requireBinary(t, "ffmpeg")
	out, err := exec.CommandContext(context.Background(), "ffmpeg", append([]string{"-y", "-v", "error"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// muxAudioTracks builds a Matroska file whose audio tracks carry the given
// language tags; an empty tag leaves the track untagged. Matroska, unlike
// MP4, writes no language at all when none is set.
func muxAudioTracks(t *testing.T, tags ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "tracks.mkv")
	var args []string
	for range tags {
		args = append(args, "-f", "lavfi", "-t", "3", "-i", "sine=frequency=440")
	}
	for i := range tags {
		args = append(args, "-map", strconv.Itoa(i))
	}
	for i, tag := range tags {
		if tag != "" {
			args = append(args, "-metadata:s:a:"+strconv.Itoa(i), "language="+tag)
		}
	}
	args = append(args, out)
	runFFmpeg(t, args...)
	return out
}

// wavPCMDuration validates the WAV container (16 kHz mono 16-bit PCM, sizes
// consistent with the payload) and returns the audio length it holds.
func wavPCMDuration(t *testing.T, wav []byte) time.Duration {
	t.Helper()
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		t.Fatalf("not a canonical WAV file: %d bytes", len(wav))
	}
	channels := binary.LittleEndian.Uint16(wav[22:24])
	rate := binary.LittleEndian.Uint32(wav[24:28])
	bits := binary.LittleEndian.Uint16(wav[34:36])
	if channels != 1 || rate != audiosource.SampleRate || bits != 16 {
		t.Fatalf("WAV format = %d ch, %d Hz, %d bit; want 1 ch, %d Hz, 16 bit", channels, rate, bits, audiosource.SampleRate)
	}
	dataLen := int(binary.LittleEndian.Uint32(wav[40:44]))
	if dataLen != len(wav)-44 {
		t.Fatalf("WAV data size field = %d, payload = %d", dataLen, len(wav)-44)
	}
	if riffLen := int(binary.LittleEndian.Uint32(wav[4:8])); riffLen != len(wav)-8 {
		t.Fatalf("WAV RIFF size field = %d, want %d", riffLen, len(wav)-8)
	}
	return time.Duration(dataLen/2) * time.Second / audiosource.SampleRate
}

func assertDuration(t *testing.T, got, want time.Duration) {
	t.Helper()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > durationTolerance {
		t.Errorf("extracted audio duration = %v, want %v (±%v)", got, want, durationTolerance)
	}
}

func TestFFSource_AudioStreams_FixtureReportsUntaggedStream(t *testing.T) {
	requireBinary(t, "ffprobe")

	streams, err := audiosource.NewFFSource().AudioStreams(context.Background(), fixtureVideo)
	if err != nil {
		t.Fatalf("AudioStreams: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("got %d audio streams, want 1: %+v", len(streams), streams)
	}
	// The fixture is MP4, which writes "und" for an unset language; it must
	// be reported as untagged, and the video stream at index 0 skipped.
	if streams[0].Index != 1 || !streams[0].Untagged() {
		t.Errorf("stream = %+v, want index 1 and untagged", streams[0])
	}
}

func TestFFSource_AudioStreams_ListsIndexAndLanguageTags(t *testing.T) {
	requireBinary(t, "ffprobe")
	video := muxAudioTracks(t, "eng", "", "por")

	streams, err := audiosource.NewFFSource().AudioStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("AudioStreams: %v", err)
	}

	want := []audiosource.Stream{{Index: 0, Language: "eng"}, {Index: 1, Language: ""}, {Index: 2, Language: "por"}}
	if len(streams) != len(want) {
		t.Fatalf("got %+v, want %+v", streams, want)
	}
	for i := range want {
		if streams[i] != want[i] {
			t.Errorf("stream %d = %+v, want %+v", i, streams[i], want[i])
		}
	}
	if streams[0].Untagged() || !streams[1].Untagged() {
		t.Errorf("Untagged() = %v for eng, %v for missing tag; want false, true", streams[0].Untagged(), streams[1].Untagged())
	}
}

func TestFFSource_AudioStreams_ExplicitUndTagIsUntagged(t *testing.T) {
	requireBinary(t, "ffprobe")
	video := muxAudioTracks(t, "und")

	streams, err := audiosource.NewFFSource().AudioStreams(context.Background(), video)
	if err != nil {
		t.Fatalf("AudioStreams: %v", err)
	}
	if len(streams) != 1 || !streams[0].Untagged() {
		t.Errorf("streams = %+v, want one untagged stream", streams)
	}
}

func TestFFSource_AudioStreams_VideoWithoutAudioYieldsNoStreams(t *testing.T) {
	requireBinary(t, "ffprobe")
	silent := filepath.Join(t.TempDir(), "silent.mp4")
	runFFmpeg(t, "-i", fixtureVideo, "-an", "-c", "copy", silent)

	streams, err := audiosource.NewFFSource().AudioStreams(context.Background(), silent)
	if err != nil {
		t.Fatalf("AudioStreams: %v", err)
	}
	if len(streams) != 0 {
		t.Errorf("streams = %+v, want none", streams)
	}
}

func TestFFSource_Extract_TimeRangeHasExpectedDuration(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")

	wav, err := audiosource.NewFFSource().Extract(context.Background(), fixtureVideo, 1,
		audiosource.Range{Start: 3 * time.Second, Duration: 2 * time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDuration(t, wavPCMDuration(t, wav), 2*time.Second)
}

func TestFFSource_Extract_RangeRunningPastEndIsTruncated(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")

	wav, err := audiosource.NewFFSource().Extract(context.Background(), fixtureVideo, 1,
		audiosource.Range{Start: 7 * time.Second, Duration: 10 * time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDuration(t, wavPCMDuration(t, wav), 2*time.Second)
}

func TestFFSource_Extract_ZeroDurationExtractsWholeStream(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")

	wav, err := audiosource.NewFFSource().Extract(context.Background(), fixtureVideo, 1, audiosource.Range{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDuration(t, wavPCMDuration(t, wav), fixtureVideoDuration)
}

func TestFFSource_Extract_SelectsRequestedStream(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")
	video := muxAudioTracks(t, "eng", "por")

	wav, err := audiosource.NewFFSource().Extract(context.Background(), video, 1, audiosource.Range{Duration: time.Second})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDuration(t, wavPCMDuration(t, wav), time.Second)
}

func TestFFSource_Extract_MissingAudioStreamIsClearError(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")
	src := audiosource.NewFFSource()

	// Index 0 of the fixture is its video stream, not audio.
	for _, index := range []int{0, 5} {
		_, err := src.Extract(context.Background(), fixtureVideo, index, audiosource.Range{Duration: time.Second})
		if !errors.Is(err, audiosource.ErrNoAudioStream) {
			t.Errorf("Extract(index %d) error = %v, want ErrNoAudioStream", index, err)
		}
	}

	silent := filepath.Join(t.TempDir(), "silent.mp4")
	runFFmpeg(t, "-i", fixtureVideo, "-an", "-c", "copy", silent)
	if _, err := src.Extract(context.Background(), silent, 1, audiosource.Range{}); !errors.Is(err, audiosource.ErrNoAudioStream) {
		t.Errorf("Extract on video without audio: error = %v, want ErrNoAudioStream", err)
	}
}

func TestFFSource_Extract_UnreadableVideoIsError(t *testing.T) {
	requireBinary(t, "ffprobe")

	missing := filepath.Join(t.TempDir(), "nope.mp4")
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("test precondition: file exists")
	}
	if _, err := audiosource.NewFFSource().Extract(context.Background(), missing, 1, audiosource.Range{}); err == nil {
		t.Error("Extract on a missing file returned nil error")
	}
}

func TestStream_MatchesLanguage_BaseLanguageIncludingRegionVariants(t *testing.T) {
	cases := []struct {
		stream audiosource.Stream
		target string
		want   bool
	}{
		{audiosource.Stream{Language: "eng"}, "en", true},
		{audiosource.Stream{Language: "por"}, "pt-BR", true},
		{audiosource.Stream{Language: "por"}, "pt-PT", true},
		{audiosource.Stream{Language: "ger"}, "de", true},
		{audiosource.Stream{Language: "eng"}, "pt-BR", false},
		{audiosource.Stream{Language: ""}, "en", false},
	}
	for _, tc := range cases {
		target, err := language.Parse(tc.target)
		if err != nil {
			t.Fatalf("parsing %q: %v", tc.target, err)
		}
		if got := tc.stream.MatchesLanguage(target); got != tc.want {
			t.Errorf("Stream{%q}.MatchesLanguage(%s) = %v, want %v", tc.stream.Language, tc.target, got, tc.want)
		}
	}
}

// toneWithSilences builds a video-less Matroska file of a 440 Hz tone that
// is muted over each given [start, end) second span.
func toneWithSilences(t *testing.T, total int, muted ...[2]int) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "tone.mkv")
	filter := "volume=1"
	for _, span := range muted {
		filter += fmt.Sprintf(",volume=enable='between(t,%d,%d)':volume=0", span[0], span[1])
	}
	runFFmpeg(t, "-f", "lavfi", "-t", strconv.Itoa(total), "-i", "sine=frequency=440", "-af", filter, out)
	return out
}

func TestFFSource_Silences_ReportsMutedStretches(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")
	video := toneWithSilences(t, 10, [2]int{3, 5}, [2]int{8, 10})

	got, err := audiosource.NewFFSource().Silences(context.Background(), video, 0)
	if err != nil {
		t.Fatalf("Silences: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Silences = %+v, want 2 intervals", got)
	}
	assertDuration(t, got[0].Start, 3*time.Second)
	assertDuration(t, got[0].End, 5*time.Second)
	assertDuration(t, got[1].Start, 8*time.Second)
	// The second silence runs to the end of the stream.
	assertDuration(t, got[1].End, 10*time.Second)
}

func TestFFSource_Silences_ContinuousToneHasNone(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")

	got, err := audiosource.NewFFSource().Silences(context.Background(), muxAudioTracks(t, "eng"), 0)
	if err != nil {
		t.Fatalf("Silences: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Silences = %+v, want none", got)
	}
}

func TestFFSource_Silences_MissingAudioStreamIsClearError(t *testing.T) {
	requireBinary(t, "ffmpeg")
	requireBinary(t, "ffprobe")

	_, err := audiosource.NewFFSource().Silences(context.Background(), fixtureVideo, 0)
	if !errors.Is(err, audiosource.ErrNoAudioStream) {
		t.Errorf("Silences(video stream index) error = %v, want ErrNoAudioStream", err)
	}
}

func TestFFSource_Duration_ReportsVideoLength(t *testing.T) {
	requireBinary(t, "ffprobe")

	got, err := audiosource.NewFFSource().Duration(context.Background(), fixtureVideo)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	assertDuration(t, got, fixtureVideoDuration)
}

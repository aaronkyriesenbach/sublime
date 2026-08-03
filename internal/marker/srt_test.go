package marker_test

import (
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
	"golang.org/x/text/encoding/unicode"
)

const sampleSRT = `1
00:00:00,500 --> 00:00:01,900
One two three

2
00:00:03,500 --> 00:00:05,100
Four five six

3
00:00:06,500 --> 00:00:08,100
Seven eight nine
`

func srtCodec(t *testing.T) marker.MarkerCodec {
	t.Helper()
	codec, ok := marker.CodecFor(".srt")
	if !ok {
		t.Fatal("CodecFor(\".srt\") ok = false, want true")
	}
	return codec
}

func TestSRTCodec_RoundTrip(t *testing.T) {
	codec := srtCodec(t)
	want := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	written, err := codec.Write([]byte(sampleSRT), want)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, presence := codec.Read(written)
	if presence != marker.Present {
		t.Fatalf("Read() presence = %v, want Present", presence)
	}
	if got != want {
		t.Errorf("Read() marker = %+v, want %+v", got, want)
	}
}

func TestSRTCodec_WriteAppendsZeroDurationCueAfterLastRealCue(t *testing.T) {
	codec := srtCodec(t)
	m := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	written, err := codec.Write([]byte(sampleSRT), m)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := string(written)
	if !strings.Contains(got, "Seven eight nine") {
		t.Fatalf("Write() dropped an existing cue: %q", got)
	}

	want := "4\n00:00:08,100 --> 00:00:08,100\nSUBLIME-MARKER-V1:0123456789abcdef\n"
	if !strings.HasSuffix(strings.TrimRight(got, "\n")+"\n", want) {
		t.Errorf("Write() = %q, want it to end with marker cue %q", got, want)
	}
}

func TestSRTCodec_WriteOnEmptyContentUsesZeroTimestamp(t *testing.T) {
	codec := srtCodec(t)
	m := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	written, err := codec.Write([]byte(""), m)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, presence := codec.Read(written)
	if presence != marker.Present {
		t.Fatalf("Read() presence = %v, want Present", presence)
	}
	if got != m {
		t.Errorf("Read() marker = %+v, want %+v", got, m)
	}
	if !strings.Contains(string(written), "00:00:00,000 --> 00:00:00,000") {
		t.Errorf("Write() on empty content = %q, want a 00:00:00,000 zero-duration cue", string(written))
	}
}

func TestSRTCodec_ReadAbsentWhenNoMarker(t *testing.T) {
	codec := srtCodec(t)

	_, presence := codec.Read([]byte(sampleSRT))
	if presence != marker.Absent {
		t.Errorf("Read() presence = %v, want Absent", presence)
	}
}

func TestSRTCodec_ReadCorruptWhenMarkerHashMalformed(t *testing.T) {
	codec := srtCodec(t)
	corrupt := sampleSRT + "\n4\n00:00:08,100 --> 00:00:08,100\nSUBLIME-MARKER-V1:not-a-valid-hash\n"

	_, presence := codec.Read([]byte(corrupt))
	if presence != marker.Corrupt {
		t.Errorf("Read() presence = %v, want Corrupt", presence)
	}
}

func TestSRTCodec_ReadCorruptWhenMarkerTruncated(t *testing.T) {
	codec := srtCodec(t)
	corrupt := sampleSRT + "\n4\n00:00:08,100 --> 00:00:08,100\nSUBLIME-MARKER-V1:\n"

	_, presence := codec.Read([]byte(corrupt))
	if presence != marker.Corrupt {
		t.Errorf("Read() presence = %v, want Corrupt", presence)
	}
}

// TestSRTCodec_HashMismatchAfterVideoChanges exercises the actual purpose
// of a Marker's embedded hash: it's read back successfully, but no longer
// Matches the video's current Content Hash once the video has changed.
func TestSRTCodec_HashMismatchAfterVideoChanges(t *testing.T) {
	codec := srtCodec(t)
	original := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	written, err := codec.Write([]byte(sampleSRT), original)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, presence := codec.Read(written)
	if presence != marker.Present {
		t.Fatalf("Read() presence = %v, want Present", presence)
	}

	currentHash := media.ContentHash("fedcba9876543210")
	if got.Matches(currentHash) {
		t.Error("Matches(changed content hash) = true, want false")
	}
	if !got.Matches(original.ContentHash) {
		t.Error("Matches(original content hash) = false, want true")
	}
}

func TestSRTCodec_WriteNormalizesUTF16ToUTF8(t *testing.T) {
	codec := srtCodec(t)
	m := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	enc := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewEncoder()
	utf16Content, err := enc.String("\ufeff" + sampleSRT)
	if err != nil {
		t.Fatalf("encoding fixture as UTF-16: %v", err)
	}

	written, err := codec.Write([]byte(utf16Content), m)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if !strings.Contains(string(written), "SUBLIME-MARKER-V1:0123456789abcdef") {
		t.Fatalf("Write() output = %q, want a decoded UTF-8 marker line", string(written))
	}
	if !strings.Contains(string(written), "Seven eight nine") {
		t.Errorf("Write() output = %q, want decoded UTF-8 cue text", string(written))
	}

	got, presence := codec.Read(written)
	if presence != marker.Present {
		t.Fatalf("Read() presence = %v, want Present", presence)
	}
	if got != m {
		t.Errorf("Read() marker = %+v, want %+v", got, m)
	}
}

func TestSRTCodec_ReadNormalizesUTF16Input(t *testing.T) {
	codec := srtCodec(t)
	m := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	written, err := codec.Write([]byte(sampleSRT), m)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	enc := unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewEncoder()
	utf16Written, err := enc.Bytes(written)
	if err != nil {
		t.Fatalf("encoding fixture as UTF-16: %v", err)
	}

	got, presence := codec.Read(utf16Written)
	if presence != marker.Present {
		t.Fatalf("Read() presence = %v, want Present", presence)
	}
	if got != m {
		t.Errorf("Read() marker = %+v, want %+v", got, m)
	}
}

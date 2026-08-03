package marker_test

import (
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/marker"
	"github.com/aaronkyriesenbach/sublime/internal/media"
)

func TestMarker_Matches(t *testing.T) {
	m := marker.Marker{ContentHash: media.ContentHash("0123456789abcdef")}

	if !m.Matches(media.ContentHash("0123456789abcdef")) {
		t.Error("Matches(same hash) = false, want true")
	}
	if m.Matches(media.ContentHash("fedcba9876543210")) {
		t.Error("Matches(different hash) = true, want false")
	}
}

func TestCodecFor_SRTRegisteredByDefault(t *testing.T) {
	codec, ok := marker.CodecFor(".srt")
	if !ok {
		t.Fatal("CodecFor(\".srt\") ok = false, want true")
	}
	if codec == nil {
		t.Fatal("CodecFor(\".srt\") codec = nil, want non-nil")
	}
}

func TestCodecFor_CaseInsensitiveExtension(t *testing.T) {
	codec, ok := marker.CodecFor(".SRT")
	if !ok {
		t.Fatal("CodecFor(\".SRT\") ok = false, want true")
	}
	if codec == nil {
		t.Fatal("CodecFor(\".SRT\") codec = nil, want non-nil")
	}
}

func TestCodecFor_UnknownExtension(t *testing.T) {
	_, ok := marker.CodecFor(".ass")
	if ok {
		t.Error("CodecFor(\".ass\") ok = true, want false (no codec registered yet)")
	}
}

func TestRegisterCodec_DuplicateExtensionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterCodec with an already-registered extension did not panic")
		}
	}()

	marker.RegisterCodec(".srt", nil)
}

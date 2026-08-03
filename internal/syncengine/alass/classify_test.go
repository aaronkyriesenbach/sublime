package alass

import "testing"

func TestClassifyReference(t *testing.T) {
	tests := []struct {
		path string
		want referenceKind
	}{
		{"reference.srt", referenceKindSubtitle},
		{"reference.SRT", referenceKindSubtitle},
		{"reference.ass", referenceKindSubtitle},
		{"reference.ssa", referenceKindSubtitle},
		{"reference.idx", referenceKindSubtitle},
		{"reference.sub", referenceKindSubtitle},
		{"reference.vob", referenceKindSubtitle},
		{"reference.mp4", referenceKindVideo},
		{"reference.mkv", referenceKindVideo},
		{"reference.avi", referenceKindVideo},
		{"reference", referenceKindVideo},
	}

	for _, tt := range tests {
		if got := classifyReference(tt.path); got != tt.want {
			t.Errorf("classifyReference(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

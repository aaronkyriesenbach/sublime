package opensubtitles

import "testing"

func TestParseCosmetics_FullMatch(t *testing.T) {
	source, group, resolution, codec := parseCosmetics("Arrival.2016.1080p.BluRay.x264-GROUP")
	if source != "BluRay" || group != "GROUP" || resolution != "1080p" || codec != "x264" {
		t.Errorf("got (%q,%q,%q,%q), want (BluRay,GROUP,1080p,x264)", source, group, resolution, codec)
	}
}

func TestParseCosmetics_NoRecognizedTokens(t *testing.T) {
	source, group, resolution, codec := parseCosmetics("Some Unrelated Release Name")
	if source != "" || group != "" || resolution != "" || codec != "" {
		t.Errorf("got (%q,%q,%q,%q), want all empty", source, group, resolution, codec)
	}
}

func TestParseCosmetics_SuffixIsItselfRecognizedTag_NoReleaseGroupExtracted(t *testing.T) {
	// The token after the last hyphen ("x265") is itself a recognized
	// codec tag, not a release group — extractCosmeticReleaseGroup must
	// leave the whole string as the searchable remainder instead of
	// misreading it as a group name. Other, separately dot-delimited
	// tokens ("1080p") are still found normally by the tokenizer pass.
	source, group, resolution, _ := parseCosmetics("Show.S01E02.1080p.x264-x265")
	if group != "" {
		t.Errorf("releaseGroup = %q, want empty (suffix is a recognized tag, not a group)", group)
	}
	if resolution != "1080p" {
		t.Errorf("resolution = %q, want 1080p", resolution)
	}
	if source != "" {
		t.Errorf("source = %q, want empty", source)
	}
}

func TestParseCosmetics_LastSegmentContainsSpace_NoReleaseGroupExtracted(t *testing.T) {
	group, _ := extractCosmeticReleaseGroup("Arrival (2016) - Director's Cut")
	if group != "" {
		t.Errorf("releaseGroup = %q, want empty for a natural-language hyphen", group)
	}
}

func TestParseCosmetics_WholeSegmentIsRecognizedTag_NoReleaseGroupExtracted(t *testing.T) {
	group, _ := extractCosmeticReleaseGroup("Arrival.2016.WEB-DL")
	if group != "" {
		t.Errorf("releaseGroup = %q, want empty when the whole segment is itself a recognized tag", group)
	}
}

func TestParseCosmetics_EmptyRelease(t *testing.T) {
	source, group, resolution, codec := parseCosmetics("")
	if source != "" || group != "" || resolution != "" || codec != "" {
		t.Errorf("got (%q,%q,%q,%q), want all empty for an empty release string", source, group, resolution, codec)
	}
}

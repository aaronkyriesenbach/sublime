package strip_test

import (
	"testing"

	"golang.org/x/text/language"

	"github.com/aaronkyriesenbach/sublime/internal/strip"
)

func mustLang(t *testing.T, tag string) language.Tag {
	t.Helper()
	parsed, err := language.Parse(tag)
	if err != nil {
		t.Fatalf("parsing language tag %q: %v", tag, err)
	}
	return parsed
}

func TestMatchesISO6392_KnownCodeMatchesSameBaseLanguage(t *testing.T) {
	if !strip.MatchesISO6392("eng", mustLang(t, "en")) {
		t.Error("MatchesISO6392(\"eng\", en) = false, want true")
	}
	if !strip.MatchesISO6392("eng", mustLang(t, "en-US")) {
		t.Error("MatchesISO6392(\"eng\", en-US) = false, want true (region-agnostic)")
	}
	if !strip.MatchesISO6392("por", mustLang(t, "pt-BR")) {
		t.Error("MatchesISO6392(\"por\", pt-BR) = false, want true (region-agnostic)")
	}
}

func TestMatchesISO6392_BibliographicAndTerminologyVariantsBothMatch(t *testing.T) {
	// German has distinct bibliographic ("ger") and terminology ("deu")
	// ISO 639-2 codes; both must resolve to the same BCP-47 base language.
	if !strip.MatchesISO6392("ger", mustLang(t, "de")) {
		t.Error("MatchesISO6392(\"ger\", de) = false, want true")
	}
	if !strip.MatchesISO6392("deu", mustLang(t, "de")) {
		t.Error("MatchesISO6392(\"deu\", de) = false, want true")
	}
}

func TestMatchesISO6392_DifferentLanguageDoesNotMatch(t *testing.T) {
	if strip.MatchesISO6392("eng", mustLang(t, "es")) {
		t.Error("MatchesISO6392(\"eng\", es) = true, want false")
	}
}

func TestMatchesISO6392_IsCaseInsensitive(t *testing.T) {
	if !strip.MatchesISO6392("ENG", mustLang(t, "en")) {
		t.Error("MatchesISO6392(\"ENG\", en) = false, want true")
	}
}

func TestMatchesISO6392_UnresolvableCodeFailsClosed(t *testing.T) {
	cases := []string{"", "und", "zzz", "not-a-code"}
	for _, code := range cases {
		if strip.MatchesISO6392(code, mustLang(t, "en")) {
			t.Errorf("MatchesISO6392(%q, en) = true, want false (unresolvable must fail closed)", code)
		}
	}
}

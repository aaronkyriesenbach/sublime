package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestLoad_ValidConfig(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en, pt-BR]
  - name: tv-anime
    path: /media/tv
    languages: [en]
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Libraries) != 2 {
		t.Fatalf("expected 2 libraries, got %d", len(cfg.Libraries))
	}

	movies := cfg.Libraries[0]
	if movies.Name != "movies" || movies.Path != "/media/movies" {
		t.Errorf("unexpected movies library: %+v", movies)
	}
	if len(movies.Languages) != 2 {
		t.Fatalf("expected 2 languages, got %d", len(movies.Languages))
	}
	if got := movies.Languages[0].String(); got != "en" {
		t.Errorf("expected first language 'en', got %q", got)
	}
	if got := movies.Languages[1].String(); got != "pt-BR" {
		t.Errorf("expected second language 'pt-BR', got %q", got)
	}

	anime := cfg.Libraries[1]
	if anime.Name != "tv-anime" || anime.Path != "/media/tv" {
		t.Errorf("unexpected tv-anime library: %+v", anime)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: [this is not a valid path
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for malformed YAML, got nil")
	}
}

func TestLoad_UnknownTopLevelKey(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  opensubtitles:
    apiKey: should-not-be-here
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a providers: section, got nil")
	}
}

func TestLoad_MissingLibraryName(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - path: /media/movies
    languages: [en]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a library missing a name, got nil")
	}
}

func TestLoad_MissingLibraryPath(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    languages: [en]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a library missing a path, got nil")
	}
}

func TestLoad_NoLanguages(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: []
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a library with no languages, got nil")
	}
}

func TestLoad_InvalidLanguageTag(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: ["not a real tag!"]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an invalid BCP 47 tag, got nil")
	}
}

func TestLoad_DuplicateLibraryName(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
  - name: movies
    path: /media/other-movies
    languages: [en]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a duplicate library name, got nil")
	}
}

func TestLoad_NoLibraries(t *testing.T) {
	path := writeConfig(t, `libraries: []`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an empty libraries list, got nil")
	}
}

func TestLoad_StripScopeDefaultsToAll(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := cfg.Libraries[0].StripScope; got != domain.StripScopeAll {
		t.Errorf("expected default strip_scope %q, got %q", domain.StripScopeAll, got)
	}
}

func TestLoad_StripScopePerLanguage(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
    strip_scope: per_language
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := cfg.Libraries[0].StripScope; got != domain.StripScopePerLanguage {
		t.Errorf("expected strip_scope %q, got %q", domain.StripScopePerLanguage, got)
	}
}

func TestLoad_InvalidStripScope(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
    strip_scope: everything
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an invalid strip_scope, got nil")
	}
}

func TestLoad_ProviderChainDefaultsToOpenSubtitles(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.ProviderChain) != 1 {
		t.Fatalf("expected default provider chain with 1 entry, got %d", len(cfg.ProviderChain))
	}
	if cfg.ProviderChain[0].Name != "opensubtitles" {
		t.Errorf("expected default provider %q, got %q", "opensubtitles", cfg.ProviderChain[0].Name)
	}

	if len(cfg.ProviderTiers) != 1 {
		t.Fatalf("expected default provider chain with 1 tier, got %d", len(cfg.ProviderTiers))
	}
	if tier := cfg.ProviderTiers[0].Providers; len(tier) != 1 || tier[0].Name != "opensubtitles" {
		t.Errorf("expected default tier [opensubtitles], got %+v", tier)
	}
}

func TestLoad_ProviderChainExplicit(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [opensubtitles, subdl]
  opensubtitles:
    worker_count: 4
  subdl:
    worker_count: 2
    paid: true
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.ProviderChain) != 2 {
		t.Fatalf("expected provider chain with 2 entries, got %d", len(cfg.ProviderChain))
	}
	if cfg.ProviderChain[0].Name != "opensubtitles" || cfg.ProviderChain[0].WorkerCount != 4 {
		t.Errorf("unexpected first provider: %+v", cfg.ProviderChain[0])
	}
	if cfg.ProviderChain[1].Name != "subdl" || cfg.ProviderChain[1].WorkerCount != 2 || !cfg.ProviderChain[1].Paid {
		t.Errorf("unexpected second provider: %+v", cfg.ProviderChain[1])
	}

	if len(cfg.ProviderTiers) != 2 {
		t.Fatalf("expected 2 tiers, got %d", len(cfg.ProviderTiers))
	}
	if tier := cfg.ProviderTiers[0].Providers; len(tier) != 1 || tier[0].Name != "opensubtitles" {
		t.Errorf("expected first tier [opensubtitles], got %+v", tier)
	}
	if tier := cfg.ProviderTiers[1].Providers; len(tier) != 1 || tier[0].Name != "subdl" {
		t.Errorf("expected second tier [subdl], got %+v", tier)
	}
}

func TestLoad_ProviderChainNestedTierMultipleProviders(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [[opensubtitles, subdl]]
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.ProviderChain) != 2 {
		t.Fatalf("expected flattened chain with 2 entries, got %d", len(cfg.ProviderChain))
	}
	if cfg.ProviderChain[0].Name != "opensubtitles" || cfg.ProviderChain[1].Name != "subdl" {
		t.Errorf("unexpected flattened chain order: %+v", cfg.ProviderChain)
	}

	if len(cfg.ProviderTiers) != 1 {
		t.Fatalf("expected 1 tier, got %d", len(cfg.ProviderTiers))
	}
	tier := cfg.ProviderTiers[0].Providers
	if len(tier) != 2 || tier[0].Name != "opensubtitles" || tier[1].Name != "subdl" {
		t.Errorf("expected tier [opensubtitles, subdl] in list order, got %+v", tier)
	}
}

func TestLoad_ProviderChainMultipleTiersMixedShape(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain:
    - [opensubtitles, subdl]
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.ProviderTiers) != 1 {
		t.Fatalf("expected 1 tier, got %d", len(cfg.ProviderTiers))
	}
	tier := cfg.ProviderTiers[0].Providers
	if len(tier) != 2 {
		t.Fatalf("expected 2 providers in tier, got %d", len(tier))
	}
}

func TestLoad_ProviderChainEmptyTierIsError(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [[]]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an empty tier, got nil")
	}
}

func TestLoad_ProviderChainUnknownProviderNameBare(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [not-a-real-provider]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an unknown provider name, got nil")
	}
}

func TestLoad_ProviderChainUnknownProviderNameInTier(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [[opensubtitles, not-a-real-provider]]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an unknown provider name inside a tier, got nil")
	}
}

func TestLoad_ProviderChainDuplicateNameWithinTier(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [[opensubtitles, opensubtitles]]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a duplicate provider name within a tier, got nil")
	}
}

func TestLoad_ProviderChainDuplicateNameAcrossTiers(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [[opensubtitles, subdl], [opensubtitles]]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a duplicate provider name across tiers, got nil")
	}
}

func TestLoad_ProviderChainDuplicateName(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [opensubtitles, opensubtitles]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for duplicate provider names, got nil")
	}
}

func TestLoad_ProviderChainMissingName(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [""]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for missing provider name, got nil")
	}
}

func TestLoad_ProviderChainInvalidEntryKind(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [{name: opensubtitles}]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a mapping providers.chain entry, got nil")
	}
}

func TestLoad_ProviderChainTierProviderSettingsPreserved(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [[opensubtitles, subdl]]
  opensubtitles:
    worker_count: 4
  subdl:
    worker_count: 2
    paid: true
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	tier := cfg.ProviderTiers[0].Providers
	if tier[0].WorkerCount != 4 {
		t.Errorf("expected opensubtitles worker_count 4, got %d", tier[0].WorkerCount)
	}
	if tier[1].WorkerCount != 2 || !tier[1].Paid {
		t.Errorf("expected subdl worker_count 2 and paid true, got %+v", tier[1])
	}
}

func TestLoad_ProvidersUnknownProviderLevelKey(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [opensubtitles]
  priority: [opensubtitles]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an unrecognized providers: level key, got nil")
	}
}

func TestLoad_ProvidersUnknownProviderBlockKey(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
providers:
  chain: [opensubtitles]
  opensubtitles:
    api_key: should-not-be-here
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for an unrecognized key inside a provider block, got nil")
	}
}

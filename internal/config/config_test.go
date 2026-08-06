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
}

func TestLoad_ProviderChainExplicit(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
provider_chain:
  - name: opensubtitles
    worker_count: 4
  - name: future-provider
    worker_count: 2
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
	if cfg.ProviderChain[1].Name != "future-provider" || cfg.ProviderChain[1].WorkerCount != 2 {
		t.Errorf("unexpected second provider: %+v", cfg.ProviderChain[1])
	}
}

func TestLoad_ProviderChainDuplicateName(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en]
provider_chain:
  - name: opensubtitles
  - name: opensubtitles
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
provider_chain:
  - worker_count: 4
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for missing provider name, got nil")
	}
}

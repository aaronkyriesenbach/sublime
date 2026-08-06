// Package config loads and validates Sublime's declarative config file
// (`/config/config.yaml` in production), and reads Provider secrets from
// their designated environment variables.
package config

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
)

// Config is Sublime's validated, in-memory configuration.
type Config struct {
	Libraries     []domain.Library
	ProviderChain []ProviderConfig
}

// ProviderConfig represents a single Provider in the priority-ordered chain.
type ProviderConfig struct {
	// Name identifies the Provider (e.g., "opensubtitles").
	Name string

	// WorkerCount is the number of concurrent workers allocated to this
	// Provider's pool. Zero means split evenly among all Providers.
	WorkerCount int
}

// rawConfig mirrors config.yaml's on-disk shape before validation and
// conversion into domain types. KnownFields decoding on this struct is what
// rejects a stray `providers:` section: Provider secrets come from env vars
// only (see ProviderSecrets), never the config file.
type rawConfig struct {
	Libraries     []rawLibrary      `yaml:"libraries"`
	ProviderChain []rawProviderConf `yaml:"provider_chain"`
}

type rawLibrary struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`
	Languages  []string `yaml:"languages"`
	StripScope string   `yaml:"strip_scope"`
}

type rawProviderConf struct {
	Name        string `yaml:"name"`
	WorkerCount int    `yaml:"worker_count"`
}

// Load reads, parses, and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	return fromRaw(raw)
}

func fromRaw(raw rawConfig) (*Config, error) {
	if len(raw.Libraries) == 0 {
		return nil, fmt.Errorf("config must define at least one library")
	}

	seenNames := make(map[string]struct{}, len(raw.Libraries))
	libraries := make([]domain.Library, 0, len(raw.Libraries))

	for i, rl := range raw.Libraries {
		lib, err := libraryFromRaw(rl)
		if err != nil {
			return nil, fmt.Errorf("library at index %d: %w", i, err)
		}

		if _, exists := seenNames[lib.Name]; exists {
			return nil, fmt.Errorf("duplicate library name %q", lib.Name)
		}
		seenNames[lib.Name] = struct{}{}

		libraries = append(libraries, lib)
	}

	providerChain, err := providerChainFromRaw(raw.ProviderChain)
	if err != nil {
		return nil, err
	}

	return &Config{Libraries: libraries, ProviderChain: providerChain}, nil
}

// providerChainFromRaw converts raw provider config into validated
// ProviderConfig entries, defaulting to a single opensubtitles entry
// if the chain is empty.
func providerChainFromRaw(raw []rawProviderConf) ([]ProviderConfig, error) {
	if len(raw) == 0 {
		return []ProviderConfig{{Name: "opensubtitles"}}, nil
	}

	chain := make([]ProviderConfig, 0, len(raw))
	seenNames := make(map[string]struct{}, len(raw))

	for i, rp := range raw {
		if rp.Name == "" {
			return nil, fmt.Errorf("provider_chain[%d]: missing required field %q", i, "name")
		}
		if _, exists := seenNames[rp.Name]; exists {
			return nil, fmt.Errorf("provider_chain: duplicate provider name %q", rp.Name)
		}
		seenNames[rp.Name] = struct{}{}

		if rp.WorkerCount < 0 {
			return nil, fmt.Errorf("provider_chain[%d]: worker_count cannot be negative", i)
		}

		chain = append(chain, ProviderConfig(rp))
	}

	return chain, nil
}

func libraryFromRaw(rl rawLibrary) (domain.Library, error) {
	if rl.Name == "" {
		return domain.Library{}, fmt.Errorf("missing required field %q", "name")
	}
	if rl.Path == "" {
		return domain.Library{}, fmt.Errorf("missing required field %q", "path")
	}
	if len(rl.Languages) == 0 {
		return domain.Library{}, fmt.Errorf("library %q: must list at least one language", rl.Name)
	}

	languages := make([]language.Tag, 0, len(rl.Languages))
	for _, tag := range rl.Languages {
		parsed, err := language.Parse(tag)
		if err != nil {
			return domain.Library{}, fmt.Errorf("library %q: invalid BCP 47 language tag %q: %w", rl.Name, tag, err)
		}
		languages = append(languages, parsed)
	}

	stripScope, err := stripScopeFromRaw(rl.Name, rl.StripScope)
	if err != nil {
		return domain.Library{}, err
	}

	return domain.Library{
		Name:       rl.Name,
		Path:       rl.Path,
		Languages:  languages,
		StripScope: stripScope,
	}, nil
}

// stripScopeFromRaw defaults an empty strip_scope to domain.StripScopeAll and
// rejects anything other than the two recognized values.
func stripScopeFromRaw(libraryName, raw string) (domain.StripScope, error) {
	if raw == "" {
		return domain.StripScopeAll, nil
	}

	scope := domain.StripScope(raw)
	switch scope {
	case domain.StripScopeAll, domain.StripScopePerLanguage:
		return scope, nil
	default:
		return "", fmt.Errorf("library %q: invalid strip_scope %q (must be %q or %q)",
			libraryName, raw, domain.StripScopeAll, domain.StripScopePerLanguage)
	}
}

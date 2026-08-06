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

	// Paid is SubDL-specific: whether to use SubDL's paid tier. It is
	// meaningless for other Providers.
	Paid bool
}

// rawConfig mirrors config.yaml's on-disk shape before validation and
// conversion into domain types. KnownFields decoding on this struct is what
// rejects a stray key under `providers:`: Provider secrets come from env
// vars only (see ProviderSecrets), never the config file.
type rawConfig struct {
	Libraries []rawLibrary  `yaml:"libraries"`
	Providers *rawProviders `yaml:"providers"`
}

type rawLibrary struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`
	Languages  []string `yaml:"languages"`
	StripScope string   `yaml:"strip_scope"`
}

// rawProviders mirrors the `providers:` block: an ordered Provider Chain by
// name, plus each named Provider's own settings block. Named Provider
// blocks are captured via the inline map so that KnownFields decoding still
// rejects unrecognized keys within each block (see rawProviderSettings).
type rawProviders struct {
	Chain    []string                       `yaml:"chain"`
	Settings map[string]rawProviderSettings `yaml:",inline"`
}

type rawProviderSettings struct {
	WorkerCount int  `yaml:"worker_count"`
	Paid        bool `yaml:"paid"`
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

	providerChain, err := providerChainFromRaw(raw.Providers)
	if err != nil {
		return nil, err
	}

	return &Config{Libraries: libraries, ProviderChain: providerChain}, nil
}

// providerChainFromRaw converts the raw `providers:` block into validated,
// ordered ProviderConfig entries, defaulting to a single opensubtitles
// entry if the block is absent or its chain is empty.
func providerChainFromRaw(raw *rawProviders) ([]ProviderConfig, error) {
	if raw == nil || len(raw.Chain) == 0 {
		return []ProviderConfig{{Name: "opensubtitles"}}, nil
	}

	chain := make([]ProviderConfig, 0, len(raw.Chain))
	seenNames := make(map[string]struct{}, len(raw.Chain))

	for i, name := range raw.Chain {
		if name == "" {
			return nil, fmt.Errorf("providers.chain[%d]: missing required field %q", i, "name")
		}
		if _, exists := seenNames[name]; exists {
			return nil, fmt.Errorf("providers.chain: duplicate provider name %q", name)
		}
		seenNames[name] = struct{}{}

		settings := raw.Settings[name]
		if settings.WorkerCount < 0 {
			return nil, fmt.Errorf("providers.%s: worker_count cannot be negative", name)
		}

		chain = append(chain, ProviderConfig{
			Name:        name,
			WorkerCount: settings.WorkerCount,
			Paid:        settings.Paid,
		})
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

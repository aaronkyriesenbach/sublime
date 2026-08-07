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
	Libraries []domain.Library

	// ProviderChain is the full Provider Chain flattened into strict
	// highest-priority-first order, ignoring Tier boundaries. Existing
	// consumers (internal/pipeline, internal/cli) read this field; it is
	// unaffected by nested-Tier parsing (see ProviderTiers) and preserves
	// today's exact ordering and semantics for a flat chain.
	ProviderChain []ProviderConfig

	// ProviderTiers is the same Providers as ProviderChain, grouped into
	// their configured Tiers, highest-priority Tier first (see CONTEXT.md's
	// Tier and Provider Chain entries, and ADR 0008). A bare Provider name in
	// providers.chain parses as its own one-member Tier here.
	ProviderTiers []ProviderTier
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

// ProviderTier is a named rank within the Provider Chain, holding one or
// more Providers the operator declares equally trustworthy at that
// priority level (CONTEXT.md's Tier entry, ADR 0008). List order inside a
// Tier is a soft preference only, never a strict-priority gate.
type ProviderTier struct {
	Providers []ProviderConfig
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
//
// Chain is decoded as raw yaml.Node values, not []string, because each
// entry may be either a bare Provider name (a one-member Tier) or a nested
// list of names (a Tier of several Providers) — see tierNamesFromNode.
type rawProviders struct {
	Chain    []yaml.Node                    `yaml:"chain"`
	Settings map[string]rawProviderSettings `yaml:",inline"`
}

// knownProviderNames lists every Provider name providers.chain may
// reference. Kept in sync by hand with
// internal/pipeline/production.go's providerConstructors map, which is
// where each name is wired to a concrete Provider: internal/config can't
// import internal/pipeline's provider packages without an import cycle
// (e.g. opensubtitles.Config embeds config.OpenSubtitlesSecrets).
var knownProviderNames = map[string]struct{}{
	"opensubtitles": {},
	"subdl":         {},
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

	providerChain, providerTiers, err := providerChainFromRaw(raw.Providers)
	if err != nil {
		return nil, err
	}

	return &Config{Libraries: libraries, ProviderChain: providerChain, ProviderTiers: providerTiers}, nil
}

// providerChainFromRaw converts the raw `providers:` block into a validated
// Provider Chain, both flattened (ProviderConfig entries, highest-priority
// first, ignoring Tier boundaries) and grouped into Tiers, defaulting to a
// single one-member opensubtitles Tier if the block is absent or its chain
// is empty. Each raw.Chain entry is either a bare Provider name (its own
// one-member Tier) or a nested list of names (one Tier of several
// Providers) — see tierNamesFromNode. Validates that every Tier names at
// least one Provider, every name is a known Provider, and no name repeats
// anywhere in the whole chain.
func providerChainFromRaw(raw *rawProviders) ([]ProviderConfig, []ProviderTier, error) {
	if raw == nil || len(raw.Chain) == 0 {
		defaultChain := []ProviderConfig{{Name: "opensubtitles"}}
		return defaultChain, []ProviderTier{{Providers: defaultChain}}, nil
	}

	chain := make([]ProviderConfig, 0, len(raw.Chain))
	tiers := make([]ProviderTier, 0, len(raw.Chain))
	seenNames := make(map[string]struct{}, len(raw.Chain))

	for i, node := range raw.Chain {
		names, err := tierNamesFromNode(node, i)
		if err != nil {
			return nil, nil, err
		}
		if len(names) == 0 {
			return nil, nil, fmt.Errorf("providers.chain[%d]: tier must name at least one provider", i)
		}

		tierProviders := make([]ProviderConfig, 0, len(names))
		for _, name := range names {
			if name == "" {
				return nil, nil, fmt.Errorf("providers.chain[%d]: missing required field %q", i, "name")
			}
			if _, known := knownProviderNames[name]; !known {
				return nil, nil, fmt.Errorf("providers.chain[%d]: unknown provider %q", i, name)
			}
			if _, exists := seenNames[name]; exists {
				return nil, nil, fmt.Errorf("providers.chain: duplicate provider name %q", name)
			}
			seenNames[name] = struct{}{}

			settings := raw.Settings[name]
			if settings.WorkerCount < 0 {
				return nil, nil, fmt.Errorf("providers.%s: worker_count cannot be negative", name)
			}

			provider := ProviderConfig{
				Name:        name,
				WorkerCount: settings.WorkerCount,
				Paid:        settings.Paid,
			}
			chain = append(chain, provider)
			tierProviders = append(tierProviders, provider)
		}

		tiers = append(tiers, ProviderTier{Providers: tierProviders})
	}

	return chain, tiers, nil
}

// tierNamesFromNode extracts the Provider name(s) declared by a single
// providers.chain entry: a scalar node (bare name) yields its own
// one-member Tier, a sequence node (nested list) yields every name in list
// order, and any other node kind (e.g. a mapping) is a config error.
func tierNamesFromNode(node yaml.Node, index int) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		var name string
		if err := node.Decode(&name); err != nil {
			return nil, fmt.Errorf("providers.chain[%d]: %w", index, err)
		}
		return []string{name}, nil
	case yaml.SequenceNode:
		var names []string
		if err := node.Decode(&names); err != nil {
			return nil, fmt.Errorf("providers.chain[%d]: %w", index, err)
		}
		return names, nil
	default:
		return nil, fmt.Errorf("providers.chain[%d]: must be a provider name or a list of provider names", index)
	}
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

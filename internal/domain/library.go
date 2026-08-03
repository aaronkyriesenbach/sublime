// Package domain holds Sublime's core domain types — the vocabulary shared
// across config loading, the state store, and the sync pipeline. See
// CONTEXT.md at the repo root for the canonical definitions these types
// implement.
package domain

import "golang.org/x/text/language"

// Library is a single filesystem root path (plus its subdirectories)
// registered with Sublime, containing video files whose subtitles are
// managed. A Sublime instance can watch multiple independent Libraries, each
// with its own target languages.
type Library struct {
	// Name is a stable, explicit identifier distinct from Path — used by CLI
	// commands, logs, and the HTTP API. Paths can change (remounts,
	// reorganization) without the Library's identity changing.
	Name string

	// Path is the Library's filesystem root.
	Path string

	// Languages are the target languages for this Library, as canonical
	// IETF BCP 47 tags. Provider-agnostic: each Provider adapter is
	// responsible for translating a tag into whatever its own API expects.
	Languages []language.Tag

	// StripScope controls how much of a file's existing (non-Sublime)
	// subtitles a Strip pass removes for this Library. See CONTEXT.md's
	// "Strip" entry.
	StripScope StripScope
}

// StripScope controls how much of a file's existing subtitles a Strip pass
// removes.
type StripScope string

const (
	// StripScopeAll removes every non-Sublime embedded stream and
	// non-Sublime sidecar for the file, regardless of language. This is
	// the default when a Library doesn't set StripScope explicitly.
	StripScopeAll StripScope = "all"

	// StripScopePerLanguage only touches the embedded stream(s)/sidecar(s)
	// for the language currently being fetched; other-language subtitles
	// are left alone.
	StripScopePerLanguage StripScope = "per_language"
)

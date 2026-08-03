// Package api's wire types: the JSON request/response shapes shared by
// every handler and the internal/cli thin clients that decode them. Kept
// exported and in one place so the CLI never redeclares its own copy of the
// contract (see internal/cli).
package api

// ErrorEnvelope is the uniform JSON shape for every non-2xx response.
type ErrorEnvelope struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Machine-readable error codes used in ErrorEnvelope.Error.
const (
	codeInvalidRequest   = "invalid_request"
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeInternalError    = "internal_error"
)

// LibraryEntry is one Library's full listing shape for GET /libraries: its
// config identity plus aggregate sync counts.
type LibraryEntry struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Languages []string `json:"languages"`
	Pending   int      `json:"pending"`
	Synced    int      `json:"synced"`
	Failed    int      `json:"failed"`
}

// LibrariesResponse is GET /libraries' response shape.
type LibrariesResponse struct {
	Libraries []LibraryEntry `json:"libraries"`
}

// LibrarySummaryEntry is the lighter-weight library shape /status embeds:
// aggregate counts only, without LibraryEntry's path/languages detail.
type LibrarySummaryEntry struct {
	Name    string `json:"name"`
	Pending int    `json:"pending"`
	Synced  int    `json:"synced"`
	Failed  int    `json:"failed"`
}

// LanguageStateEntry is one language's sync state within a FileEntry.
// Reason is only populated when Status is "failed".
type LanguageStateEntry struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// FileEntry is one tracked file's shape within GET /status' scoped "files"
// array.
type FileEntry struct {
	Path        string                        `json:"path"`
	Library     string                        `json:"library"`
	ContentHash string                        `json:"contentHash"`
	Languages   map[string]LanguageStateEntry `json:"languages"`
}

// StatusResponse is GET /status' response shape. Files/Total/Limit/Offset
// are only populated for a scoped request (?library= or ?path= given);
// pointers so an unscoped response omits them entirely via omitempty
// instead of emitting misleading zero values.
type StatusResponse struct {
	Libraries []LibrarySummaryEntry `json:"libraries"`
	Files     []FileEntry           `json:"files,omitempty"`
	Total     *int                  `json:"total,omitempty"`
	Limit     *int                  `json:"limit,omitempty"`
	Offset    *int                  `json:"offset,omitempty"`
}

// ReprocessRequest is POST /reprocess's request body: the file, directory,
// or Library path to forcibly reprocess.
type ReprocessRequest struct {
	Path string `json:"path"`
}

// ReprocessScope identifies what a reprocess request was scoped to.
type ReprocessScope struct {
	Path string `json:"path"`
}

// ReprocessResponse is POST /reprocess's 202 Accepted response body.
type ReprocessResponse struct {
	Accepted bool           `json:"accepted"`
	Scope    ReprocessScope `json:"scope"`
}

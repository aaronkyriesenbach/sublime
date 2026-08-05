package api

import (
	"net/http"
	"strconv"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

const (
	defaultStatusLimit = 100
	maxStatusLimit     = 1000
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "GET required")
		return
	}

	q := r.URL.Query()
	libraryParam := q.Get("library")
	pathParam := q.Get("path")
	stateParam := q.Get("state")

	var scopeLib domain.Library
	scoped := false

	if libraryParam != "" {
		lib, ok := s.libraryByName(libraryParam)
		if !ok {
			writeError(w, http.StatusNotFound, codeNotFound, "unknown library \""+libraryParam+"\"")
			return
		}
		scopeLib, scoped = lib, true
	}

	if pathParam != "" {
		lib, ok := s.libraryForPath(pathParam)
		if !ok {
			writeError(w, http.StatusNotFound, codeNotFound, "path is not part of any configured library")
			return
		}
		if scoped && lib.Name != scopeLib.Name {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "path is not under library \""+libraryParam+"\"")
			return
		}
		scopeLib, scoped = lib, true
	}

	if stateParam != "" && stateParam != "all" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, `"state" must be "all" if set`)
		return
	}

	limit := defaultStatusLimit
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, `"limit" must be a positive integer`)
			return
		}
		if v > maxStatusLimit {
			v = maxStatusLimit
		}
		limit = v
	}

	offset := 0
	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, `"offset" must be a non-negative integer`)
			return
		}
		offset = v
	}

	summaryScope := s.libraries
	if scoped {
		summaryScope = []domain.Library{scopeLib}
	}

	allSummaries, err := s.store.LibrarySummaries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternalError, "failed to load library summaries")
		return
	}

	libSummaries := make([]LibrarySummaryEntry, 0, len(summaryScope))
	for _, lib := range summaryScope {
		sum := summaryFor(allSummaries, lib.Name)
		libSummaries = append(libSummaries, LibrarySummaryEntry{
			Name:       lib.Name,
			Pending:    sum.Pending,
			InProgress: sum.InProgress,
			Synced:     sum.Synced,
			Failed:     sum.Failed,
		})
	}

	resp := StatusResponse{Libraries: libSummaries}

	if scoped {
		files, total, err := s.store.ListFiles(r.Context(), store.FileFilter{
			LibraryName:    scopeLib.Name,
			PathPrefix:     pathParam,
			IncompleteOnly: stateParam != "all",
			Limit:          limit,
			Offset:         offset,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, codeInternalError, "failed to load files")
			return
		}

		resp.Files = toFileEntries(files)
		resp.Total = &total
		resp.Limit = &limit
		resp.Offset = &offset
	}

	writeJSON(w, http.StatusOK, resp)
}

func toFileEntries(files []domain.File) []FileEntry {
	entries := make([]FileEntry, 0, len(files))
	for _, f := range files {
		languages := make(map[string]LanguageStateEntry, len(f.Languages))
		for _, ls := range f.Languages {
			entry := LanguageStateEntry{Status: string(ls.Status)}
			if ls.Status == domain.StatusFailed {
				entry.Reason = string(ls.FailureReason)
			}
			languages[ls.Language.String()] = entry
		}
		entries = append(entries, FileEntry{
			Path:        f.Path,
			Library:     f.LibraryName,
			ContentHash: f.ContentHash,
			Languages:   languages,
		})
	}
	return entries
}

package api

import "net/http"

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "GET required")
		return
	}

	summaries, err := s.store.LibrarySummaries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternalError, "failed to load library summaries")
		return
	}

	entries := make([]LibraryEntry, 0, len(s.libraries))
	for _, lib := range s.libraries {
		sum := summaryFor(summaries, lib.Name)
		entries = append(entries, LibraryEntry{
			Name:      lib.Name,
			Path:      lib.Path,
			Languages: languageTags(lib),
			Pending:   sum.Pending,
			Synced:    sum.Synced,
			Failed:    sum.Failed,
		})
	}

	writeJSON(w, http.StatusOK, LibrariesResponse{Libraries: entries})
}

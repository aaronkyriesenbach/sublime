package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Server) handleReprocess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "POST required")
		return
	}

	var req ReprocessRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, fmt.Sprintf("malformed request body: %v", err))
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, `"path" is required`)
		return
	}

	lib, ok := s.libraryForPath(req.Path)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "path is not part of any configured library")
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.reprocess(s.ctx, lib, req.Path); err != nil {
			s.logger.Error("background reprocess failed", "path", req.Path, "library", lib.Name, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, ReprocessResponse{
		Accepted: true,
		Scope:    ReprocessScope(req),
	})
}

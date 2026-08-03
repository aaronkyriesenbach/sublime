package api

import (
	"path/filepath"
	"strings"

	"github.com/aaronkyriesenbach/sublime/internal/domain"
	"github.com/aaronkyriesenbach/sublime/internal/store"
)

// languageTags renders lib's target languages as their canonical IETF BCP
// 47 string forms, in configured order.
func languageTags(lib domain.Library) []string {
	tags := make([]string, len(lib.Languages))
	for i, tag := range lib.Languages {
		tags[i] = tag.String()
	}
	return tags
}

// summaryFor returns name's LibrarySummary from summaries, or a zero-valued
// one if name has no tracked files yet.
func summaryFor(summaries []store.LibrarySummary, name string) store.LibrarySummary {
	for _, sum := range summaries {
		if sum.LibraryName == name {
			return sum
		}
	}
	return store.LibrarySummary{LibraryName: name}
}

// libraryByName finds name among s.libraries.
func (s *Server) libraryByName(name string) (domain.Library, bool) {
	for _, lib := range s.libraries {
		if lib.Name == name {
			return lib, true
		}
	}
	return domain.Library{}, false
}

// libraryForPath finds the configured Library that owns path: the Library
// whose Path equals path, or is a directory ancestor of it. If more than
// one Library's Path would match (nested Libraries), the longest (most
// specific) match wins.
func (s *Server) libraryForPath(path string) (domain.Library, bool) {
	clean := filepath.Clean(path)

	var best domain.Library
	found := false
	for _, lib := range s.libraries {
		libClean := filepath.Clean(lib.Path)
		if clean != libClean && !strings.HasPrefix(clean, libClean+string(filepath.Separator)) {
			continue
		}
		if !found || len(libClean) > len(filepath.Clean(best.Path)) {
			best = lib
			found = true
		}
	}
	return best, found
}

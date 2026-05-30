package server

import (
	"net/http"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/translate"
)

// projects returns the default project plus one per Proxmox resource pool. When
// there are no pools this is just the single default "proxmox" project.
func (s *Server) projects(r *http.Request) []oxide.Project {
	out := []oxide.Project{translate.ProjectFromPool("", "")}
	for _, p := range s.poolList(r.Context()) {
		out = append(out, translate.ProjectFromPool(p.PoolID, p.Comment))
	}
	return out
}

func (s *Server) handleProjectList(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(s.projects(r)))
}

func (s *Server) handleProjectView(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("project")
	for _, p := range s.projects(r) {
		if ref == p.Name || ref == p.ID {
			oxide.WriteJSON(w, http.StatusOK, p)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "project not found: "+ref)
}

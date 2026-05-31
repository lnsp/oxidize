package server

import (
	"context"
	"net/http"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

// projects returns one project per Proxmox resource pool, plus the synthetic
// default "proxmox" project — but only when there are VMs not in any pool, so
// the fallback project disappears once everything is organized into pools.
func (s *Server) projects(r *http.Request) []oxide.Project {
	var out []oxide.Project
	if s.hasUnpooledVMs(r.Context()) {
		out = append(out, translate.ProjectFromPool("", ""))
	}
	for _, p := range s.poolList(r.Context()) {
		out = append(out, translate.ProjectFromPool(p.PoolID, p.Comment))
	}
	return out
}

// hasUnpooledVMs reports whether any VM is not a member of a resource pool,
// which is what the default project surfaces.
func (s *Server) hasUnpooledVMs(ctx context.Context) bool {
	vms, err := s.listVMs(ctx)
	if err != nil {
		// On error, keep the default project so VMs never vanish from the UI.
		return true
	}
	for _, vm := range vms {
		if vm.Pool == "" {
			return true
		}
	}
	return false
}

// projectIDFromRef resolves a ?project= NameOrId to its Oxide project id,
// defaulting to the default project.
func (s *Server) projectIDFromRef(ctx context.Context, ref string) string {
	if pool, scoped := s.projectPool(ctx, ref); scoped && pool != "" {
		return translate.ProjectIDForPool(pool)
	}
	return translate.ProjectID
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

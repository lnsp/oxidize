package server

import (
	"context"
	"encoding/json"
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

// resolvePool maps a project ref to its Proxmox pool. ok=false means no real
// pool matched (caller should 404); isDefault=true means the ref targets the
// synthetic default project, which is not a real pool and cannot be mutated.
func (s *Server) resolvePool(ctx context.Context, ref string) (pool string, isDefault, ok bool) {
	pool, scoped := s.projectPool(ctx, ref)
	if !scoped {
		return "", false, false
	}
	if pool == "" {
		return "", true, true // the default "proxmox" project
	}
	return pool, false, true
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		oxide.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Name == translate.DefaultProjectName {
		oxide.WriteError(w, http.StatusBadRequest, "project name is reserved: "+body.Name)
		return
	}
	poolid, ok := translate.PoolIDFromName(body.Name)
	if !ok {
		oxide.WriteError(w, http.StatusBadRequest, "name cannot form a valid Proxmox pool id: "+body.Name)
		return
	}
	if err := s.pve.CreatePool(r.Context(), poolid, body.Description); err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, translate.ProjectFromPool(poolid, body.Description))
}

func (s *Server) handleProjectUpdate(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("project")
	pool, isDefault, ok := s.resolvePool(r.Context(), ref)
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "project not found: "+ref)
		return
	}
	if isDefault {
		oxide.WriteError(w, http.StatusBadRequest, "the default project cannot be modified")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Proxmox poolids are immutable, so a project rename is not possible. Accept a
	// name that resolves to the same pool (an idempotent no-op) but reject one
	// that would change it.
	if body.Name != nil {
		if newid, ok := translate.PoolIDFromName(*body.Name); !ok || newid != pool {
			oxide.WriteError(w, http.StatusBadRequest, "project name cannot be changed: Proxmox pool ids are immutable")
			return
		}
	}
	desc := ""
	if body.Description != nil {
		desc = *body.Description
	}
	if err := s.pve.UpdatePoolComment(r.Context(), pool, desc); err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, translate.ProjectFromPool(pool, desc))
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("project")
	pool, isDefault, ok := s.resolvePool(r.Context(), ref)
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "project not found: "+ref)
		return
	}
	if isDefault {
		oxide.WriteError(w, http.StatusBadRequest, "the default project cannot be deleted")
		return
	}
	// Deleting a non-empty pool would orphan its member VMs; refuse so the user
	// removes or reassigns them first.
	vms, err := s.vmsForProject(r.Context(), ref)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if len(vms) > 0 {
		oxide.WriteError(w, http.StatusBadRequest, "project is not empty: it still has instances")
		return
	}
	if err := s.pve.DeletePool(r.Context(), pool); err != nil {
		writeProxmoxError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

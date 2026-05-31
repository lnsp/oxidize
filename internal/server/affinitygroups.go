package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/store"
	"github.com/lnsp/oxidize/internal/translate"
)

// Affinity and anti-affinity groups are oxidize-owned, store-backed state. The
// same handlers serve both surfaces, parameterized by kind: /v1/affinity-groups
// uses store.KindAffinity and /v1/anti-affinity-groups uses
// store.KindAntiAffinity. The groups and their membership are recorded and
// surfaced to the console (so the affinity pages are functional CRUD), but
// placement is NOT enforced — Proxmox owns VM placement and has no equivalent
// concept. See internal/store.AffinityGroupStore for the full caveat.

// affinityGroupHandlers binds a set of handlers to a group kind (affinity vs
// anti-affinity), so the two identical API surfaces share one implementation.
type affinityGroupHandlers struct {
	s    *Server
	kind string
}

// affinityGroupToOxide adapts a persisted store record to the translate
// package's record view (which can't import store) and renders the Oxide shape.
func affinityGroupToOxide(g store.AffinityGroup) oxide.AffinityGroup {
	return translate.AffinityGroupToOxide(translate.AffinityGroupRecord{
		ID:            g.ID,
		Name:          g.Name,
		Description:   g.Description,
		ProjectID:     g.ProjectID,
		Policy:        g.Policy,
		FailureDomain: g.FailureDomain,
		TimeCreated:   g.TimeCreated,
		TimeModified:  g.TimeModified,
	})
}

func (s *Server) affinityGroups() affinityGroupHandlers {
	return affinityGroupHandlers{s: s, kind: store.KindAffinity}
}

func (s *Server) antiAffinityGroups() affinityGroupHandlers {
	return affinityGroupHandlers{s: s, kind: store.KindAntiAffinity}
}

func (h affinityGroupHandlers) list(w http.ResponseWriter, r *http.Request) {
	projectID := h.s.projectIDFromRef(r.Context(), r.URL.Query().Get("project"))
	groups, err := h.s.affgroups.List(h.kind, projectID)
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]oxide.AffinityGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, affinityGroupToOxide(g))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (h affinityGroupHandlers) view(w http.ResponseWriter, r *http.Request) {
	g, ok, err := h.s.affgroups.Get(h.kind, r.PathValue("group"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, affinityGroupToOxide(g))
}

type affinityGroupCreateBody struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Policy        string `json:"policy"`         // "allow" | "fail"
	FailureDomain string `json:"failure_domain"` // "sled"
}

func (h affinityGroupHandlers) create(w http.ResponseWriter, r *http.Request) {
	var body affinityGroupCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		oxide.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	// The only valid policy/failure_domain values in the Oxide API; default them
	// so a minimal request still yields a well-formed record.
	if body.Policy == "" {
		body.Policy = "allow"
	}
	if body.Policy != "allow" && body.Policy != "fail" {
		oxide.WriteError(w, http.StatusBadRequest, "policy must be \"allow\" or \"fail\"")
		return
	}
	if body.FailureDomain == "" {
		body.FailureDomain = "sled"
	}
	projectID := h.s.projectIDFromRef(r.Context(), r.URL.Query().Get("project"))
	now := time.Now().UTC()
	rec := store.AffinityGroup{
		ID:            translate.AffinityGroupID(h.kind, projectID, body.Name),
		Kind:          h.kind,
		Name:          body.Name,
		Description:   body.Description,
		ProjectID:     projectID,
		Policy:        body.Policy,
		FailureDomain: body.FailureDomain,
		TimeCreated:   now,
		TimeModified:  now,
	}
	if err := h.s.affgroups.Create(rec); err != nil {
		if err == store.ErrGroupNameTaken {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, affinityGroupToOxide(rec))
}

func (h affinityGroupHandlers) update(w http.ResponseWriter, r *http.Request) {
	// The Oxide AffinityGroupUpdate exposes description (and name, which we can't
	// honor: a rename would change the derived id). We additionally accept policy,
	// the only other metadata Proxmox can't enforce but we can record.
	var body struct {
		Description *string `json:"description"`
		Policy      *string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Policy != nil && *body.Policy != "allow" && *body.Policy != "fail" {
		oxide.WriteError(w, http.StatusBadRequest, "policy must be \"allow\" or \"fail\"")
		return
	}
	g, ok, err := h.s.affgroups.Update(h.kind, r.PathValue("group"), body.Description, body.Policy, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, affinityGroupToOxide(g))
}

func (h affinityGroupHandlers) delete(w http.ResponseWriter, r *http.Request) {
	ok, err := h.s.affgroups.Delete(h.kind, r.PathValue("group"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h affinityGroupHandlers) memberList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, ok, err := h.s.affgroups.Get(h.kind, r.PathValue("group"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	out := []oxide.AffinityGroupMember{}
	for _, vmid := range g.Members {
		out = append(out, h.s.affinityMember(ctx, vmid))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (h affinityGroupHandlers) memberView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, ok, err := h.s.affgroups.Get(h.kind, r.PathValue("group"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	ref, err := h.s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	for _, vmid := range g.Members {
		if vmid == ref.vmid {
			oxide.WriteJSON(w, http.StatusOK, h.s.affinityMember(ctx, vmid))
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "instance is not a member of this group")
}

func (h affinityGroupHandlers) memberAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := h.s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	_, ok, err := h.s.affgroups.AddMember(h.kind, r.PathValue("group"), ref.vmid, time.Now().UTC())
	// Re-adding an existing member is not an error: return the member as if added.
	if err != nil && err != store.ErrMemberExists {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, h.s.affinityMember(ctx, ref.vmid))
}

func (h affinityGroupHandlers) memberDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := h.s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	ok, err := h.s.affgroups.RemoveMember(h.kind, r.PathValue("group"), ref.vmid, time.Now().UTC())
	if err == store.ErrMemberNotFound {
		oxide.WriteError(w, http.StatusNotFound, "instance is not a member of this group")
		return
	}
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// affinityMember builds the group-member payload for an instance vmid, resolving
// the instance's current name and run state. A vmid that no longer resolves
// (e.g. the VM was deleted out-of-band) still yields a stable id-only member so
// the membership list doesn't break.
func (s *Server) affinityMember(ctx context.Context, vmid int) oxide.AffinityGroupMember {
	val := oxide.AffinityGroupMemberValue{
		ID:       translate.InstanceID(vmid),
		Name:     "vm-" + strconv.Itoa(vmid),
		RunState: oxide.StateStopped,
	}
	if vms, err := s.listVMs(ctx); err == nil {
		for _, vm := range vms {
			if vm.VMID == vmid {
				inst := translate.InstanceFromResource(vm)
				val.Name = inst.Name
				val.RunState = inst.RunState
				if st, serr := s.pve.QemuStatus(ctx, vm.Node, vmid); serr == nil {
					translate.ApplyStatus(&inst, st)
					val.RunState = inst.RunState
				}
				break
			}
		}
	}
	return oxide.AffinityGroupMember{Type: "instance", Value: val}
}

// reverse-lookup handlers for the per-instance group lists
// (GET /v1/instances/{instance}/affinity-groups and .../anti-affinity-groups).

func (h affinityGroupHandlers) instanceGroupList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := h.s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	groups, err := h.s.affgroups.GroupsForInstance(h.kind, "", ref.vmid)
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]oxide.AffinityGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, affinityGroupToOxide(g))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

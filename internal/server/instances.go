package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/translate"
)

// writeProxmoxError maps an upstream Proxmox error to an Oxide error response.
func writeProxmoxError(w http.ResponseWriter, err error) {
	var pe *proxmox.Error
	if errors.As(err, &pe) {
		status := pe.Status
		// Surface upstream auth/permission as a 502 rather than logging the
		// user out of the console (their session here is still valid).
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			status = http.StatusBadGateway
		}
		if status < 400 {
			status = http.StatusBadGateway
		}
		oxide.WriteError(w, status, pe.Msg)
		return
	}
	oxide.WriteError(w, http.StatusBadGateway, err.Error())
}

// vmRef identifies a located Proxmox VM.
type vmRef struct {
	node string
	vmid int
	res  proxmox.ClusterResource
}

// listVMs returns all non-template QEMU VMs across all nodes. It uses the
// per-node qemu listing rather than /cluster/resources because the latter is
// cache-delayed and won't reflect a just-created or just-deleted VM.
func (s *Server) listVMs(ctx context.Context) ([]proxmox.ClusterResource, error) {
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	var vms []proxmox.ClusterResource
	for _, n := range nodes {
		entries, err := s.pve.NodeQemu(ctx, n.Node)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Template != 0 {
				continue
			}
			vms = append(vms, proxmox.ClusterResource{
				Type: "qemu", VMID: e.VMID, Node: n.Node, Name: e.Name,
				Status: e.Status, MaxMem: e.MaxMem, MaxCPU: e.CPUs,
				MaxDisk: e.MaxDisk, Uptime: e.Uptime, Lock: e.Lock,
			})
		}
	}
	// Overlay resource-pool membership (used to group VMs into projects). This
	// comes from /cluster/resources, which carries the pool field; it's slightly
	// cache-delayed but pool membership changes rarely.
	if pools := s.poolMembership(ctx); len(pools) > 0 {
		for i := range vms {
			vms[i].Pool = pools[vms[i].VMID]
		}
	}
	// Stable order by vmid — nodes and per-node listings come back in arbitrary
	// order, which otherwise reshuffles the instances table on every reload.
	sort.Slice(vms, func(i, j int) bool { return vms[i].VMID < vms[j].VMID })
	return vms, nil
}

// poolMembership returns a vmid -> pool name map from /cluster/resources.
func (s *Server) poolMembership(ctx context.Context) map[int]string {
	res, err := s.pve.ClusterResources(ctx, "vm")
	if err != nil {
		return nil
	}
	out := make(map[int]string, len(res))
	for _, r := range res {
		if r.Pool != "" {
			out[r.VMID] = r.Pool
		}
	}
	return out
}

// vmsForProject returns the VMs belonging to the given project ref. The default
// project ("proxmox") contains VMs not in any pool; a pool project contains its
// members. An empty/unknown ref defaults to all VMs.
func (s *Server) vmsForProject(ctx context.Context, projectRef string) ([]proxmox.ClusterResource, error) {
	vms, err := s.listVMs(ctx)
	if err != nil {
		return nil, err
	}
	pool, scoped := s.projectPool(ctx, projectRef)
	if !scoped {
		return vms, nil
	}
	out := vms[:0]
	for _, vm := range vms {
		if vm.Pool == pool {
			out = append(out, vm)
		}
	}
	return out, nil
}

// projectPool resolves a project NameOrId to its pool name. Returns scoped=false
// when the ref is empty or unrecognized (caller should not filter).
func (s *Server) projectPool(ctx context.Context, ref string) (pool string, scoped bool) {
	if ref == "" {
		return "", false
	}
	if ref == translate.DefaultProjectName || ref == translate.ProjectID {
		return "", true // default project: unpooled VMs
	}
	for _, p := range s.poolList(ctx) {
		if p.PoolID == ref || translate.ProjectIDForPool(p.PoolID) == ref ||
			translate.SanitizeName(p.PoolID, "pool") == ref {
			return p.PoolID, true
		}
	}
	return "", false
}

// poolList returns the cluster's pools (empty on error).
func (s *Server) poolList(ctx context.Context) []proxmox.Pool {
	pools, err := s.pve.Pools(ctx)
	if err != nil {
		return nil
	}
	return pools
}

// resolveInstance locates a VM by its Oxide id or sanitized name.
func (s *Server) resolveInstance(ctx context.Context, ref string) (*vmRef, error) {
	vms, err := s.listVMs(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range vms {
		name := translate.SanitizeName(r.Name, "vm-"+strconv.Itoa(r.VMID))
		if ref == translate.InstanceID(r.VMID) || ref == name {
			return &vmRef{node: r.Node, vmid: r.VMID, res: r}, nil
		}
	}
	return nil, nil
}

func (s *Server) handleInstanceList(w http.ResponseWriter, r *http.Request) {
	vms, err := s.vmsForProject(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	out := make([]oxide.Instance, 0, len(vms))
	for _, vm := range vms {
		out = append(out, translate.InstanceFromResource(vm))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleInstanceView(w http.ResponseWriter, r *http.Request) {
	ref, err := s.resolveInstance(r.Context(), r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.instanceDetail(r.Context(), ref))
}

// instanceDetail builds a fully enriched instance (status + config). It works
// even when ref.res is empty (e.g. right after create) by deriving identity
// from the vmid and the live status/config.
func (s *Server) instanceDetail(ctx context.Context, ref *vmRef) oxide.Instance {
	inst := translate.InstanceFromResource(ref.res)
	// Ensure identity is correct even when ref.res wasn't populated.
	inst.ID = translate.InstanceID(ref.vmid)
	inst.ProjectID = translate.ProjectID
	inst.Description = "Proxmox VM " + strconv.Itoa(ref.vmid)
	if st, err := s.pve.QemuStatus(ctx, ref.node, ref.vmid); err == nil {
		translate.ApplyStatus(&inst, st)
		if inst.Name == "" && st.Name != "" {
			inst.Name = translate.SanitizeName(st.Name, "vm-"+strconv.Itoa(ref.vmid))
		}
	}
	if cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid); err == nil {
		translate.ApplyConfig(&inst, ref.vmid, cfg)
		if name := cfg["name"]; name != "" {
			inst.Name = translate.SanitizeName(name, "vm-"+strconv.Itoa(ref.vmid))
		}
	}
	if inst.Name == "" {
		inst.Name = "vm-" + strconv.Itoa(ref.vmid)
	}
	if inst.Hostname == "" {
		inst.Hostname = inst.Name
	}
	return inst
}

// instanceAction returns a handler that performs a Proxmox lifecycle action and
// returns the (updated) instance. We block briefly on the resulting task so the
// console sees a fresh run_state; on timeout we return an interim state and let
// the console's polling converge.
func (s *Server) instanceAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
		if err != nil {
			writeProxmoxError(w, err)
			return
		}
		if ref == nil {
			oxide.WriteError(w, http.StatusNotFound, "instance not found")
			return
		}
		upid, err := s.pve.QemuAction(ctx, ref.node, ref.vmid, action)
		if err != nil {
			writeProxmoxError(w, err)
			return
		}
		_ = s.pve.PollTask(ctx, ref.node, upid, pveTimeout)

		inst := translate.InstanceFromResource(ref.res)
		if st, err := s.pve.QemuStatus(ctx, ref.node, ref.vmid); err == nil {
			translate.ApplyStatus(&inst, st)
		} else {
			inst.RunState = interimState(action)
		}
		oxide.WriteJSON(w, http.StatusOK, inst)
	}
}

func interimState(action string) oxide.InstanceState {
	switch action {
	case "start":
		return oxide.StateStarting
	case "shutdown", "stop":
		return oxide.StateStopping
	case "reboot", "reset":
		return oxide.StateRebooting
	default:
		return oxide.StateStopped
	}
}

func (s *Server) handleInstanceDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	upid, err := s.pve.QemuDelete(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	_ = s.pve.PollTask(ctx, ref.node, upid, pveTimeout)
	// Release ephemeral IPs bound to the VM and detach any floating IPs.
	s.releaseInstanceFloatingIPs(ref.vmid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInstanceDisks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	disks := translate.DisksFromConfig(ref.vmid, translate.InstanceID(ref.vmid), translate.ProjectIDForPool(ref.res.Pool), cfg)
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(disks))
}

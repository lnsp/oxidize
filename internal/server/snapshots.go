package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/translate"
)

// allSnapshots aggregates Proxmox VM snapshots across all VMs as Oxide
// snapshots. Proxmox snapshots are VM-level; each is associated with the VM's
// first (boot) disk.
func (s *Server) allSnapshots(ctx context.Context) ([]oxide.Snapshot, error) {
	vms, err := s.listVMs(ctx)
	if err != nil {
		return nil, err
	}
	return s.snapshotsForVMs(ctx, vms), nil
}

func (s *Server) snapshotsForVMs(ctx context.Context, vms []proxmox.ClusterResource) []oxide.Snapshot {
	var out []oxide.Snapshot
	for _, vm := range vms {
		snaps, err := s.pve.Snapshots(ctx, vm.Node, vm.VMID)
		if err != nil {
			continue
		}
		// "current" is the live state, not a real snapshot.
		real := snaps[:0]
		for _, sn := range snaps {
			if sn.Name != "current" {
				real = append(real, sn)
			}
		}
		if len(real) == 0 {
			continue
		}
		diskID := s.bootDiskID(ctx, vm.Node, vm.VMID)
		for _, sn := range real {
			out = append(out, translate.SnapshotFromProxmox(vm.VMID, diskID, vm.MaxDisk, sn))
		}
	}
	return out
}

// bootDiskID returns the id of a VM's first disk, or "" if none.
func (s *Server) bootDiskID(ctx context.Context, node string, vmid int) string {
	cfg, err := s.pve.QemuConfig(ctx, node, vmid)
	if err != nil {
		return ""
	}
	disks := translate.DisksFromConfig(vmid, translate.InstanceID(vmid), translate.ProjectID, cfg)
	if len(disks) > 0 {
		return disks[0].ID
	}
	return ""
}

func (s *Server) handleSnapshotList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vms, err := s.vmsForProject(ctx, r.URL.Query().Get("project"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(s.snapshotsForVMs(ctx, vms)))
}

func (s *Server) handleSnapshotView(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("snapshot")
	snaps, err := s.allSnapshots(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	for _, sn := range snaps {
		if sn.ID == ref || sn.Name == ref {
			oxide.WriteJSON(w, http.StatusOK, sn)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "snapshot not found: "+ref)
}

type snapshotCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Disk        string `json:"disk"` // NameOrId of the disk to snapshot
}

func (s *Server) handleSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body snapshotCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		oxide.WriteError(w, http.StatusBadRequest, "snapshot name is required")
		return
	}
	ref, diskID, err := s.resolveDiskVM(ctx, body.Disk)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusBadRequest, "disk not found or not attached to a VM: "+body.Disk)
		return
	}
	upid, err := s.pve.Snapshot(ctx, ref.node, ref.vmid, body.Name, body.Description)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if err := s.pve.PollTask(ctx, ref.node, upid, pveTimeout); err != nil && err != context.DeadlineExceeded {
		writeProxmoxError(w, err)
		return
	}
	snap := translate.SnapshotFromProxmox(ref.vmid, diskID, ref.res.MaxDisk,
		proxmox.QemuSnapshot{Name: body.Name, Description: body.Description})
	oxide.WriteJSON(w, http.StatusCreated, snap)
}

func (s *Server) handleSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref := r.PathValue("snapshot")
	// Find which VM + snapshot name this id/name refers to.
	vms, err := s.listVMs(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	for _, vm := range vms {
		snaps, err := s.pve.Snapshots(ctx, vm.Node, vm.VMID)
		if err != nil {
			continue
		}
		for _, sn := range snaps {
			if sn.Name == "current" {
				continue
			}
			if translate.SnapshotID(vm.VMID, sn.Name) == ref || sn.Name == ref {
				upid, derr := s.pve.DeleteSnapshot(ctx, vm.Node, vm.VMID, sn.Name)
				if derr != nil {
					writeProxmoxError(w, derr)
					return
				}
				_ = s.pve.PollTask(ctx, vm.Node, upid, pveTimeout)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "snapshot not found: "+ref)
}

// resolveDiskVM finds the VM an attached disk belongs to, returning the vmRef
// and the disk's Oxide id.
func (s *Server) resolveDiskVM(ctx context.Context, diskRef string) (*vmRef, string, error) {
	vms, err := s.listVMs(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, vm := range vms {
		cfg, err := s.pve.QemuConfig(ctx, vm.Node, vm.VMID)
		if err != nil {
			continue
		}
		for _, d := range translate.DisksFromConfig(vm.VMID, translate.InstanceID(vm.VMID), translate.ProjectID, cfg) {
			if d.ID == diskRef || d.Name == diskRef {
				ref := &vmRef{node: vm.Node, vmid: vm.VMID, res: vm}
				return ref, d.ID, nil
			}
		}
	}
	return nil, "", nil
}

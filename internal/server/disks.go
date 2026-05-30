package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/proxmox"
	"github.com/lennart/oxidize/internal/translate"
)

// disksForVMs builds the attached disks for a set of VMs.
func (s *Server) disksForVMs(ctx context.Context, vms []proxmox.ClusterResource) []oxide.Disk {
	var disks []oxide.Disk
	for _, vm := range vms {
		cfg, err := s.pve.QemuConfig(ctx, vm.Node, vm.VMID)
		if err != nil {
			continue // skip VMs whose config we can't read
		}
		disks = append(disks, translate.DisksFromConfig(vm.VMID, translate.InstanceID(vm.VMID), translate.ProjectIDForPool(vm.Pool), cfg)...)
	}
	return disks
}

// allDisks aggregates disks across every VM (attached) plus standalone volumes
// in the oxidize disk pool (unattached, created via POST /v1/disks).
func (s *Server) allDisks(ctx context.Context) ([]oxide.Disk, error) {
	vms, err := s.listVMs(ctx)
	if err != nil {
		return nil, err
	}
	disks := s.disksForVMs(ctx, vms)
	for _, v := range mustPool(s.poolVolumes(ctx)) {
		disks = append(disks, translate.StandaloneDisk(v.VolID, v.Size))
	}
	return disks, nil
}

func (s *Server) handleDiskList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := r.URL.Query().Get("project")
	vms, err := s.vmsForProject(ctx, project)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	disks := s.disksForVMs(ctx, vms)
	// Standalone (pool) disks belong to the default project.
	if pool, scoped := s.projectPool(ctx, project); !scoped || pool == "" {
		for _, v := range mustPool(s.poolVolumes(ctx)) {
			disks = append(disks, translate.StandaloneDisk(v.VolID, v.Size))
		}
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(disks))
}

func (s *Server) handleDiskView(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("disk")
	disks, err := s.allDisks(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	for _, d := range disks {
		if d.ID == ref || d.Name == ref {
			oxide.WriteJSON(w, http.StatusOK, d)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "disk not found: "+ref)
}

type diskCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Size        int64  `json:"size"` // bytes
}

// handleDiskCreate allocates a standalone disk volume in the oxidize disk pool.
// Proxmox has no first-class unattached disk, so we allocate it under a reserved
// VMID namespace; it can later be attached to an instance in Proxmox.
func (s *Server) handleDiskCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body diskCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	node, err := s.firstNode(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	storage, err := s.imagesStorage(ctx, node)
	if err != nil {
		oxide.WriteError(w, http.StatusBadGateway, "no images-capable storage found")
		return
	}
	sizeGB := int((body.Size + (1024*1024*1024 - 1)) / (1024 * 1024 * 1024))
	if sizeGB < 1 {
		sizeGB = 1
	}
	idx, err := s.nextPoolDiskIndex(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	filename := fmt.Sprintf("vm-%d-disk-%d", translate.DiskPoolVMID, idx)
	volid, err := s.pve.AllocVolume(ctx, node, storage, translate.DiskPoolVMID, sizeGB, filename)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	disk := translate.StandaloneDisk(volid, int64(sizeGB)*1024*1024*1024)
	oxide.WriteJSON(w, http.StatusCreated, disk)
}

// handleDiskDelete frees a standalone pool disk. Attached disks must be removed
// via their instance in Proxmox and are reported as a conflict here.
func (s *Server) handleDiskDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref := r.PathValue("disk")
	pool, err := s.poolVolumes(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	for _, v := range pool {
		if translate.VolumeDiskID(v.VolID) == ref || matchesVolName(v.VolID, ref) {
			upid, derr := s.pve.DeleteVolume(ctx, v.Node, v.VolID)
			if derr != nil {
				writeProxmoxError(w, derr)
				return
			}
			_ = s.pve.PollTask(ctx, v.Node, upid, pveTimeout)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	oxide.WriteError(w, http.StatusBadRequest,
		"only standalone disks can be deleted here; detach disks from their instance in Proxmox first")
}

// poolVolume is a standalone volume in the disk pool.
type poolVolume struct {
	VolID string
	Size  int64
	Node  string
}

// poolVolumes lists volumes allocated in the oxidize disk pool across nodes.
func (s *Server) poolVolumes(ctx context.Context) ([]poolVolume, error) {
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []poolVolume
	for _, n := range nodes {
		storages, err := s.pve.Storages(ctx, n.Node)
		if err != nil {
			continue
		}
		for _, st := range storages {
			if !strings.Contains(st.Content, "images") {
				continue
			}
			vols, err := s.pve.StorageContent(ctx, n.Node, st.Storage, "images")
			if err != nil {
				continue
			}
			for _, v := range vols {
				if v.VMID == translate.DiskPoolVMID && !seen[v.VolID] {
					seen[v.VolID] = true
					out = append(out, poolVolume{VolID: v.VolID, Size: v.Size, Node: n.Node})
				}
			}
		}
	}
	return out, nil
}

// nextPoolDiskIndex returns the next free disk index in the pool namespace.
func (s *Server) nextPoolDiskIndex(ctx context.Context) (int, error) {
	vols, err := s.poolVolumes(ctx)
	if err != nil {
		return 0, err
	}
	max := -1
	prefix := fmt.Sprintf("vm-%d-disk-", translate.DiskPoolVMID)
	for _, v := range vols {
		name := v.VolID
		if _, after, ok := strings.Cut(v.VolID, ":"); ok {
			name = after
		}
		if strings.HasPrefix(name, prefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(name, prefix)); err == nil && n > max {
				max = n
			}
		}
	}
	return max + 1, nil
}

func matchesVolName(volid, ref string) bool {
	name := volid
	if _, after, ok := strings.Cut(volid, ":"); ok {
		name = after
	}
	return translate.SanitizeName(name, "disk") == ref
}

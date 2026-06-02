package server

import (
	"context"
	"net/http"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

func syntheticSilo() oxide.Silo {
	return oxide.Silo{
		ID:               translate.SiloID,
		Name:             "proxmox",
		Description:      "Proxmox VE cluster",
		Discoverable:     true,
		IdentityMode:     "local_only",
		MappedFleetRoles: map[string][]string{},
		AdminGroupName:   nil,
		TimeCreated:      epochTime(),
		TimeModified:     epochTime(),
	}
}

// handlePing answers the unauthenticated health check (`oxide ping`). The
// spec's Ping schema is a single required "status" enum that is always "ok".
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRackList(w http.ResponseWriter, r *http.Request) {
	rack := oxide.Rack{ID: translate.RackID, TimeCreated: epochTime(), TimeModified: epochTime()}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.Rack{rack}))
}

func (s *Server) handleSledList(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.pve.Nodes(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	sleds := make([]oxide.Sled, 0, len(nodes))
	for _, n := range nodes {
		sleds = append(sleds, translate.SledFromNode(n))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(sleds))
}

// resolveSledNode maps a sled id (or node name) back to its Proxmox node.
func (s *Server) resolveSledNode(ctx context.Context, ref string) string {
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		if translate.SledID(n.Node) == ref || n.Node == ref {
			return n.Node
		}
	}
	return ""
}

func (s *Server) handleSledView(w http.ResponseWriter, r *http.Request) {
	node := s.resolveSledNode(r.Context(), r.PathValue("sledId"))
	if node == "" {
		oxide.WriteError(w, http.StatusNotFound, "sled not found")
		return
	}
	nodes, _ := s.pve.Nodes(r.Context())
	for _, n := range nodes {
		if n.Node == node {
			oxide.WriteJSON(w, http.StatusOK, translate.SledFromNode(n))
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "sled not found")
}

func (s *Server) handleSledInstanceList(w http.ResponseWriter, r *http.Request) {
	node := s.resolveSledNode(r.Context(), r.PathValue("sledId"))
	if node == "" {
		oxide.WriteError(w, http.StatusNotFound, "sled not found")
		return
	}
	vms, err := s.listVMs(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	out := make([]oxide.SledInstance, 0)
	for _, vm := range vms {
		if vm.Node == node {
			out = append(out, translate.SledInstanceFromResource(node, vm))
		}
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handlePhysicalDiskList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	out := make([]oxide.PhysicalDisk, 0)
	for _, n := range nodes {
		disks, err := s.pve.NodeDisks(ctx, n.Node)
		if err != nil {
			continue
		}
		for _, d := range disks {
			out = append(out, translate.PhysicalDiskFromNode(n.Node, d))
		}
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleSiloList(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.Silo{syntheticSilo()}))
}

func (s *Server) handleSiloView(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("silo")
	silo := syntheticSilo()
	if ref != silo.Name && ref != silo.ID {
		oxide.WriteError(w, http.StatusNotFound, "silo not found: "+ref)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, silo)
}

// clusterUtilization sums capacity (from nodes) and provisioned (from VMs).
func (s *Server) clusterUtilization(ctx context.Context) (capacity, provisioned oxide.VirtualResourceCounts, err error) {
	res, err := s.pve.ClusterResources(ctx, "")
	if err != nil {
		return capacity, provisioned, err
	}
	for _, r := range res {
		switch r.Type {
		case "node":
			capacity.CPUs += int64(r.MaxCPU)
			capacity.Memory += r.MaxMem
			capacity.Storage += r.MaxDisk
		case "qemu", "lxc":
			provisioned.CPUs += int64(r.MaxCPU)
			provisioned.Memory += r.MaxMem
			provisioned.Storage += r.MaxDisk
		}
	}
	return capacity, provisioned, nil
}

func (s *Server) handleUtilization(w http.ResponseWriter, r *http.Request) {
	cap, prov, err := s.clusterUtilization(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Utilization{Capacity: cap, Provisioned: prov})
}

func (s *Server) handleSiloUtilizationList(w http.ResponseWriter, r *http.Request) {
	cap, prov, err := s.clusterUtilization(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	su := oxide.SiloUtilization{
		Allocated:   cap,
		Provisioned: prov,
		SiloID:      translate.SiloID,
		SiloName:    "proxmox",
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.SiloUtilization{su}))
}

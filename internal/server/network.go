package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/translate"
)

// --- synthetic VPC / subnet / IP pool (so NICs and external IPs resolve) ---

func (s *Server) handleVpcList(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.Vpc{translate.SyntheticVPC()}))
}

func (s *Server) handleVpcView(w http.ResponseWriter, r *http.Request) {
	v := translate.SyntheticVPC()
	if ref := r.PathValue("vpc"); ref != v.ID && ref != v.Name {
		oxide.WriteError(w, http.StatusNotFound, "vpc not found: "+ref)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, v)
}

// handleFirewallRules returns the VPC firewall rules. This endpoint is NOT
// paginated — it returns a {rules: [...]} object — so it can't use the
// empty-page catch-all (the console reads data.rules.length).
func (s *Server) handleFirewallRules(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, map[string]any{"rules": []any{}})
}

func (s *Server) handleVpcSubnetList(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.VpcSubnet{translate.SyntheticSubnet()}))
}

func (s *Server) handleVpcSubnetView(w http.ResponseWriter, r *http.Request) {
	sub := translate.SyntheticSubnet()
	if ref := r.PathValue("subnet"); ref != sub.ID && ref != sub.Name {
		oxide.WriteError(w, http.StatusNotFound, "subnet not found: "+ref)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, sub)
}

func syntheticIPPool() oxide.IpPool {
	return oxide.IpPool{
		ID:           translate.IPPoolID,
		Name:         "default",
		Description:  "Proxmox network",
		TimeCreated:  epochTime(),
		TimeModified: epochTime(),
	}
}

func (s *Server) handleIPPoolList(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.IpPool{syntheticIPPool()}))
}

func (s *Server) handleIPPoolView(w http.ResponseWriter, r *http.Request) {
	p := syntheticIPPool()
	if ref := r.PathValue("pool"); ref != p.ID && ref != p.Name {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found: "+ref)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, p)
}

// --- network interfaces (mapped from Proxmox netN devices) ---

// instanceFromQuery resolves the ?instance= query param to a vmRef.
func (s *Server) instanceFromQuery(r *http.Request) (*vmRef, error) {
	return s.resolveInstance(r.Context(), r.URL.Query().Get("instance"))
}

func (s *Server) handleNICList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.instanceFromQuery(r)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.NetworkInterface{}))
		return
	}
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	macToIP := s.pve.AgentMACtoIPv4(ctx, ref.node, ref.vmid)
	nics := translate.NICsFromConfig(ref.vmid, cfg, macToIP)
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(nics))
}

func (s *Server) handleInstanceExternalIPList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteJSON(w, http.StatusOK, oxide.Page([]any{}))
		return
	}
	var ips []any
	for _, ip := range s.pve.AgentIPv4s(ctx, ref.node, ref.vmid) {
		ips = append(ips, translate.EphemeralExternalIP(ip))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(ips))
}

// handleNICCreate adds a virtio NIC to the VM on the default bridge.
func (s *Server) handleNICCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.instanceFromQuery(r)
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	dev := nextFreeIndexed(cfg, "net")
	bridge := s.firstBridge(ctx, ref.node)
	form := url.Values{}
	form.Set(dev, "virtio,bridge="+bridge)
	if _, err := s.pve.UpdateConfig(ctx, ref.node, ref.vmid, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	newCfg, _ := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	macToIP := s.pve.AgentMACtoIPv4(ctx, ref.node, ref.vmid)
	oxide.WriteJSON(w, http.StatusCreated,
		translate.NICFromConfig(ref.vmid, dev, newCfg[dev], macToIP))
}

// handleNICDelete removes a NIC device from the VM.
func (s *Server) handleNICDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.instanceFromQuery(r)
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	dev := s.nicDevice(ctx, ref, r.PathValue("interface"))
	if dev == "" {
		oxide.WriteError(w, http.StatusNotFound, "network interface not found")
		return
	}
	form := url.Values{}
	form.Set("delete", dev)
	if _, err := s.pve.UpdateConfig(ctx, ref.node, ref.vmid, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNICUpdate is a best-effort no-op that returns the current NIC (Proxmox
// NICs have no name/primary concept to update).
func (s *Server) handleNICUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.instanceFromQuery(r)
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	dev := s.nicDevice(ctx, ref, r.PathValue("interface"))
	if dev == "" {
		oxide.WriteError(w, http.StatusNotFound, "network interface not found")
		return
	}
	cfg, _ := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	macToIP := s.pve.AgentMACtoIPv4(ctx, ref.node, ref.vmid)
	oxide.WriteJSON(w, http.StatusOK, translate.NICFromConfig(ref.vmid, dev, cfg[dev], macToIP))
}

// nicDevice resolves a NIC NameOrId to its Proxmox device key (net0, ...).
func (s *Server) nicDevice(ctx context.Context, ref *vmRef, nicRef string) string {
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		return ""
	}
	for i := 0; i < 32; i++ {
		dev := "net" + strconv.Itoa(i)
		if _, ok := cfg[dev]; !ok {
			continue
		}
		if translate.NICID(ref.vmid, dev) == nicRef || dev == nicRef {
			return dev
		}
	}
	return ""
}

// nextFreeIndexed returns the first free "<prefix>N" key not present in cfg.
func nextFreeIndexed(cfg map[string]string, prefix string) string {
	for i := 0; i < 32; i++ {
		k := prefix + strconv.Itoa(i)
		if _, ok := cfg[k]; !ok {
			return k
		}
	}
	return prefix + "0"
}

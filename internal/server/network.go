package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

// --- VPCs / subnets (default = flat vmbr0 LAN; others = Proxmox SDN zones) ---

// vpcsForProject returns the default VPC plus one per SDN zone, scoped to the
// project. SDN zones are cluster-global, so they appear in every project.
func (s *Server) vpcsForProject(ctx context.Context, projectRef string, topo sdnTopo) []oxide.Vpc {
	projectID := s.projectIDFromRef(ctx, projectRef)
	vpcs := []oxide.Vpc{translate.DefaultVPC(projectID)}
	for _, z := range topo.zones {
		vpcs = append(vpcs, translate.VPCFromZone(z.Zone, "", projectID))
	}
	return vpcs
}

func (s *Server) handleVpcList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	topo := s.sdnTopology(ctx)
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(s.vpcsForProject(ctx, r.URL.Query().Get("project"), topo)))
}

func (s *Server) handleVpcView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	topo := s.sdnTopology(ctx)
	ref := r.PathValue("vpc")
	for _, v := range s.vpcsForProject(ctx, r.URL.Query().Get("project"), topo) {
		if ref == v.ID || ref == v.Name {
			oxide.WriteJSON(w, http.StatusOK, v)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "vpc not found: "+ref)
}

// VPC firewall rules are oxidize-owned, store-backed state. Oxide's VPC-scoped
// firewall model does not map cleanly or safely onto Proxmox's per-VM/cluster
// firewall, so a VPC's rule set is RECORDED and round-trips to the console (the
// firewall-rules page is fully functional: edit, save, reload) but is NOT
// applied to the Proxmox data plane. See internal/store.FirewallRuleStore for
// the full caveat. This matches the rest of the synthetic VPC surface.

// vpcIDForRef resolves a ?vpc= NameOrId to a synthetic vpc_id: the default VPC's
// id for the flat LAN, or the zone-derived id for an SDN-backed VPC. It returns
// ok=false if the ref matches no known VPC.
func (s *Server) vpcIDForRef(vpcRef string, topo sdnTopo) (string, bool) {
	dv := translate.SyntheticVPC()
	if vpcRef == "" || vpcRef == dv.ID || vpcRef == dv.Name {
		return dv.ID, true
	}
	for _, z := range topo.zones {
		if vpcRef == translate.VPCIDForZone(z.Zone) || vpcRef == translate.SanitizeName(z.Zone, "zone") {
			return translate.VPCIDForZone(z.Zone), true
		}
	}
	return "", false
}

// handleFirewallRules returns a VPC's firewall rules from the store. This
// endpoint is NOT paginated — it returns a {rules: [...]} object — so it can't
// use the empty-page catch-all (the console reads data.rules.length).
func (s *Server) handleFirewallRules(w http.ResponseWriter, r *http.Request) {
	topo := s.sdnTopology(r.Context())
	vpcID, ok := s.vpcIDForRef(r.URL.Query().Get("vpc"), topo)
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "vpc not found")
		return
	}
	rules, err := s.fwrules.Get(vpcID)
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusOK, map[string]json.RawMessage{"rules": rules})
}

// handleFirewallRulesUpdate replaces a VPC's entire firewall rule set (Oxide
// update semantics) and returns the resulting {rules: [...]}. Each rule is
// assigned a deterministic id (stable for the same vpc+name) plus the VPC id and
// timestamps; the rules are recorded but not enforced on Proxmox.
func (s *Server) handleFirewallRulesUpdate(w http.ResponseWriter, r *http.Request) {
	topo := s.sdnTopology(r.Context())
	vpcID, ok := s.vpcIDForRef(r.URL.Query().Get("vpc"), topo)
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "vpc not found")
		return
	}
	var body struct {
		Rules []oxide.VpcFirewallRuleUpdate `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	now := time.Now().UTC()
	out := make([]oxide.VpcFirewallRule, 0, len(body.Rules))
	for _, u := range body.Rules {
		if u.Action != "allow" && u.Action != "deny" {
			oxide.WriteError(w, http.StatusBadRequest, "action must be allow or deny")
			return
		}
		if u.Direction != "inbound" && u.Direction != "outbound" {
			oxide.WriteError(w, http.StatusBadRequest, "direction must be inbound or outbound")
			return
		}
		if u.Status != "enabled" && u.Status != "disabled" {
			oxide.WriteError(w, http.StatusBadRequest, "status must be enabled or disabled")
			return
		}
		out = append(out, oxide.VpcFirewallRule{
			ID:           translate.FirewallRuleID(vpcID, u.Name),
			Name:         u.Name,
			Description:  u.Description,
			Action:       u.Action,
			Direction:    u.Direction,
			Priority:     u.Priority,
			Status:       u.Status,
			Filters:      u.Filters,
			Targets:      u.Targets,
			VpcID:        vpcID,
			TimeCreated:  now,
			TimeModified: now,
		})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.fwrules.Replace(vpcID, raw); err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusOK, map[string]json.RawMessage{"rules": raw})
}

// subnetsForVPC returns the subnets of a VPC: the flat LAN subnet for the default
// VPC, or one Oxide subnet per vnet for an SDN zone VPC.
func (s *Server) subnetsForVPC(vpcRef string, topo sdnTopo) []oxide.VpcSubnet {
	dv := translate.SyntheticVPC()
	if vpcRef == "" || vpcRef == dv.ID || vpcRef == dv.Name {
		return []oxide.VpcSubnet{translate.SyntheticSubnet()}
	}
	for _, z := range topo.zones {
		if vpcRef == translate.VPCIDForZone(z.Zone) || vpcRef == translate.SanitizeName(z.Zone, "zone") {
			var out []oxide.VpcSubnet
			vpcID := translate.VPCIDForZone(z.Zone)
			for _, v := range topo.vnetsInZone(z.Zone) {
				out = append(out, translate.SubnetFromVnet(v.Vnet, topo.cidrOf(v.Vnet), vpcID))
			}
			return out
		}
	}
	return nil
}

func (s *Server) handleVpcSubnetList(w http.ResponseWriter, r *http.Request) {
	topo := s.sdnTopology(r.Context())
	subnets := s.subnetsForVPC(r.URL.Query().Get("vpc"), topo)
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(subnets))
}

func (s *Server) handleVpcSubnetView(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("subnet")
	ds := translate.SyntheticSubnet()
	if ref == ds.ID || ref == ds.Name {
		oxide.WriteJSON(w, http.StatusOK, ds)
		return
	}
	topo := s.sdnTopology(r.Context())
	for _, v := range topo.vnets {
		sub := translate.SubnetFromVnet(v.Vnet, topo.cidrOf(v.Vnet), translate.VPCIDForZone(v.Zone))
		if ref == sub.ID || ref == sub.Name {
			oxide.WriteJSON(w, http.StatusOK, sub)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "subnet not found: "+ref)
}

// handleVpcSubnetCreate creates a real SDN vnet+subnet on takahe for the VPC
// (zone). The subnet is private (snat=1) with a gateway + DHCP, mirroring lab0.
// Proxmox vnet names are constrained (<=8 chars), so the Oxide subnet name is
// derived from the vnet name rather than the requested name verbatim.
func (s *Server) handleVpcSubnetCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	topo := s.sdnTopology(ctx)
	zone := s.zoneForVPCRef(r.URL.Query().Get("vpc"), topo)
	if zone == "" {
		oxide.WriteError(w, http.StatusBadRequest,
			"subnets can only be created in an SDN-backed VPC, not the default flat LAN")
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		IPv4Block   string `json:"ipv4_block"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	_, ipnet, err := net.ParseCIDR(body.IPv4Block)
	if err != nil || ipnet.IP.To4() == nil {
		oxide.WriteError(w, http.StatusBadRequest, "ipv4_block must be a valid IPv4 CIDR")
		return
	}
	gw, dhcpStart, dhcpEnd, ok := subnetGatewayDHCP(ipnet)
	if !ok {
		oxide.WriteError(w, http.StatusBadRequest, "subnet is too small to host a gateway + DHCP range")
		return
	}
	vnet := s.vnetNameFor(firstNonEmpty(body.Name, "net"), topo)

	if err := s.pve.SDNCreateVnet(ctx, vnet, zone); err != nil {
		writeProxmoxError(w, err)
		return
	}
	form := url.Values{}
	form.Set("type", "subnet")
	form.Set("subnet", ipnet.String())
	form.Set("gateway", gw)
	form.Set("snat", "1")
	form.Set("dhcp-range", "start-address="+dhcpStart+",end-address="+dhcpEnd)
	if err := s.pve.SDNCreateSubnet(ctx, vnet, form); err != nil {
		_ = s.pve.SDNDeleteVnet(ctx, vnet) // roll back the orphan vnet
		_ = s.pve.SDNApply(ctx)
		writeProxmoxError(w, err)
		return
	}
	if err := s.pve.SDNApply(ctx); err != nil {
		writeProxmoxError(w, err)
		return
	}
	s.invalidateSDNTopology()
	oxide.WriteJSON(w, http.StatusCreated,
		translate.SubnetFromVnet(vnet, ipnet.String(), translate.VPCIDForZone(zone)))
}

// handleVpcSubnetDelete removes the SDN vnet (and its subnet) backing a subnet.
func (s *Server) handleVpcSubnetDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref := r.PathValue("subnet")
	topo := s.sdnTopology(ctx)
	for _, v := range topo.vnets {
		if ref == translate.SubnetIDForVnet(v.Vnet) || ref == translate.SanitizeName(v.Vnet, "vnet") || ref == v.Vnet {
			for _, sub := range topo.subnets[v.Vnet] {
				_, _ = s.pve.Delete(ctx, "cluster/sdn/vnets/"+v.Vnet+"/subnets/"+sub.Subnet)
			}
			if err := s.pve.SDNDeleteVnet(ctx, v.Vnet); err != nil {
				writeProxmoxError(w, err)
				return
			}
			_ = s.pve.SDNApply(ctx)
			s.invalidateSDNTopology()
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "subnet not found: "+ref)
}

// zoneForVPCRef resolves a ?vpc= ref to an SDN zone, or "" for the default VPC
// (the flat LAN, which has no SDN zone).
func (s *Server) zoneForVPCRef(vpcRef string, topo sdnTopo) string {
	dv := translate.SyntheticVPC()
	if vpcRef == "" || vpcRef == dv.ID || vpcRef == dv.Name {
		return ""
	}
	for _, z := range topo.zones {
		if vpcRef == translate.VPCIDForZone(z.Zone) || vpcRef == translate.SanitizeName(z.Zone, "zone") {
			return z.Zone
		}
	}
	return ""
}

// vnetNameFor derives a unique Proxmox vnet name (<=8 chars, alphanumeric,
// letter-led) from the requested subnet name.
func (s *Server) vnetNameFor(requested string, topo sdnTopo) string {
	base := sanitizeVnet(requested)
	existing := map[string]bool{}
	for _, v := range topo.vnets {
		existing[v.Vnet] = true
	}
	name := base
	for i := 0; existing[name]; i++ {
		suffix := strconv.Itoa(i)
		trim := 8 - len(suffix)
		if trim > len(base) {
			trim = len(base)
		}
		name = base[:trim] + suffix
	}
	return name
}

// sanitizeVnet reduces a string to a valid <=8-char vnet/bridge name.
func sanitizeVnet(in string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(in) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "v" + s
	}
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

// subnetGatewayDHCP computes the gateway (first host) and a DHCP range for a
// CIDR. Returns ok=false for subnets too small to be useful.
func subnetGatewayDHCP(ipnet *net.IPNet) (gateway, dhcpStart, dhcpEnd string, ok bool) {
	netU, valid := ipToU32(ipnet.IP)
	if !valid {
		return "", "", "", false
	}
	ones, bits := ipnet.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	if size < 8 {
		return "", "", "", false
	}
	gw := netU + 1
	start := netU + 10
	if size <= 16 {
		start = netU + 2
	}
	end := netU + size - 2 // last usable (size-1 is broadcast)
	if start > end {
		start = netU + 2
	}
	return u32ToIP(gw), u32ToIP(start), u32ToIP(end), true
}

// IP pool handlers (handleIPPoolList / handleIPPoolView, silo-scoped) and the
// system IP pool admin endpoints live in ippools.go.

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
	macToIP := s.macToIPv4(ctx, ref.node, ref.vmid)
	loc := s.sdnTopology(ctx).netLocator()
	nics := translate.NICsFromConfig(ref.vmid, cfg, macToIP, loc)
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(nics))
}

// macToIPv4 maps NIC MACs (lowercased) to IPv4 addresses, preferring the live
// guest-agent view and falling back to Proxmox SDN IPAM. IPAM allocations
// persist for stopped and agent-less VMs, so an SDN VM's private address still
// resolves on the Networking tab even when the guest agent can't report it.
func (s *Server) macToIPv4(ctx context.Context, node string, vmid int) map[string]string {
	m := s.pve.AgentMACtoIPv4(ctx, node, vmid)
	if entries, err := s.pve.SDNIPAMStatus(ctx); err == nil {
		for _, e := range entries {
			if e.MAC == "" || e.IP == "" {
				continue
			}
			if mac := strings.ToLower(e.MAC); m[mac] == "" {
				m[mac] = e.IP
			}
		}
	}
	return m
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
	// External IPs are only genuinely-allocated external addresses bound to this
	// instance (floating IPs + explicitly-attached ephemeral IPs from a pool). A
	// NIC's own address is the interface's *private* IP and belongs to the
	// network-interface list — it is deliberately NOT echoed here, so a flat-LAN
	// VM's single LAN address doesn't appear as both a private and an external IP.
	if fips, err := s.fips.List(); err == nil {
		for _, f := range fips {
			if f.InstanceVMID != ref.vmid {
				continue
			}
			if f.Ephemeral {
				ips = append(ips, map[string]any{"kind": "ephemeral", "ip": f.IP, "ip_pool_id": f.PoolID})
			} else {
				ips = append(ips, map[string]any{
					"kind": "floating", "id": f.ID, "name": f.Name, "description": f.Description,
					"ip": f.IP, "ip_pool_id": f.PoolID, "instance_id": translate.InstanceID(f.InstanceVMID),
					"project_id": f.ProjectID, "time_created": f.TimeCreated, "time_modified": f.TimeModified,
				})
			}
		}
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(ips))
}

// handleNICCreate adds a virtio NIC to the VM. The chosen Oxide subnet selects
// the Proxmox bridge: an SDN-zone subnet attaches to that vnet's bridge, the
// default subnet attaches to the flat LAN bridge (vmbr0).
func (s *Server) handleNICCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.instanceFromQuery(r)
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	var body struct {
		VpcName    string `json:"vpc_name"`
		SubnetName string `json:"subnet_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	topo := s.sdnTopology(ctx)
	bridge := s.firstBridge(ctx, ref.node)
	if vnet := topo.vnetBridge(body.SubnetName); vnet != "" {
		bridge = vnet
	}
	dev := nextFreeIndexed(cfg, "net")
	form := url.Values{}
	form.Set(dev, "virtio,bridge="+bridge)
	if _, err := s.pve.UpdateConfig(ctx, ref.node, ref.vmid, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	newCfg, _ := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	macToIP := s.macToIPv4(ctx, ref.node, ref.vmid)
	oxide.WriteJSON(w, http.StatusCreated,
		translate.NICFromConfig(ref.vmid, dev, newCfg[dev], macToIP, topo.netLocator()))
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
	macToIP := s.macToIPv4(ctx, ref.node, ref.vmid)
	loc := s.sdnTopology(ctx).netLocator()
	oxide.WriteJSON(w, http.StatusOK, translate.NICFromConfig(ref.vmid, dev, cfg[dev], macToIP, loc))
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

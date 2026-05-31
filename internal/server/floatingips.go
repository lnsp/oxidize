package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/store"
	"github.com/lnsp/oxidize/internal/translate"
)

// Floating IPs are reserved addresses in the SDN subnet that takahe DNATs to the
// attached instance's private IP (see deploy/oxidize-fip-reconcile on the host).
// oxidize owns the allocations + attachments; the host reconciler polls
// /internal/floating-ip-map and maintains the nftables rules.

func (s *Server) floatingToOxide(f store.FloatingIP) oxide.FloatingIp {
	var inst *string
	if f.InstanceVMID != 0 {
		id := translate.InstanceID(f.InstanceVMID)
		inst = &id
	}
	poolID := f.PoolID
	if poolID == "" {
		poolID = translate.IPPoolID
	}
	return oxide.FloatingIp{
		ID:           f.ID,
		Name:         f.Name,
		Description:  f.Description,
		IP:           f.IP,
		IPPoolID:     poolID,
		ProjectID:    f.ProjectID,
		InstanceID:   inst,
		TimeCreated:  f.TimeCreated,
		TimeModified: f.TimeModified,
	}
}

func (s *Server) handleFloatingIPList(w http.ResponseWriter, r *http.Request) {
	fips, err := s.fips.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	projectID := s.projectIDFromRef(r.Context(), r.URL.Query().Get("project"))
	out := []oxide.FloatingIp{}
	for _, f := range fips {
		if f.ProjectID == projectID {
			out = append(out, s.floatingToOxide(f))
		}
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleFloatingIPView(w http.ResponseWriter, r *http.Request) {
	f, ok, err := s.fips.Get(r.PathValue("floatingIp"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "floating IP not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.floatingToOxide(f))
}

type floatingIPCreateBody struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	AddressAllocator *struct {
		Type         string `json:"type"` // "explicit" | "auto"
		IP           string `json:"ip"`
		PoolSelector *struct {
			Type string `json:"type"` // "explicit" | "auto"
			Pool string `json:"pool"`
		} `json:"pool_selector"`
	} `json:"address_allocator"`
}

func (s *Server) handleFloatingIPCreate(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	var body floatingIPCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		oxide.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	pools, err := s.ippools.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Resolve the pool: an explicitly named pool, else the silo's default
	// unicast IPv4 pool.
	var pool store.IPPool
	if a := body.AddressAllocator; a != nil && a.PoolSelector != nil && a.PoolSelector.Type == "explicit" && a.PoolSelector.Pool != "" {
		p, ok, _ := s.ippools.Get(a.PoolSelector.Pool)
		if !ok {
			oxide.WriteError(w, http.StatusBadRequest, "ip pool not found: "+a.PoolSelector.Pool)
			return
		}
		pool = p
	} else if p, ok := defaultUnicastV4Pool(pools); ok {
		pool = p
	} else {
		oxide.WriteError(w, http.StatusBadRequest, "no default IP pool available")
		return
	}

	// Build the address picker: an explicit in-pool address, or the first free
	// address in the pool. The store runs it under its lock so selection and
	// persistence are atomic (no two requests can claim the same address).
	var pick func(used map[string]bool) string
	if a := body.AddressAllocator; a != nil && a.Type == "explicit" && a.IP != "" {
		if !ipInPool(pool, a.IP) {
			oxide.WriteError(w, http.StatusBadRequest, "requested address is outside the pool's ranges")
			return
		}
		reqIP := a.IP
		pick = func(map[string]bool) string { return reqIP }
	} else {
		pick = func(used map[string]bool) string { return allocateFromPool(pool, used) }
	}

	now := time.Now().UTC()
	rec, err := s.fips.CreateAllocated(store.FloatingIP{
		ID:           translate.UUIDv5("floating-ip:" + body.Name),
		Name:         body.Name,
		Description:  body.Description,
		PoolID:       pool.ID,
		ProjectID:    s.projectIDFromRef(r.Context(), r.URL.Query().Get("project")),
		TimeCreated:  now,
		TimeModified: now,
	}, pick)
	if err != nil {
		if err == store.ErrNameTaken || err == store.ErrIPTaken || err == store.ErrNoFreeIP {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, s.floatingToOxide(rec))
}

func (s *Server) handleFloatingIPDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.fips.Delete(r.PathValue("floatingIp"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "floating IP not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type floatingIPAttachBody struct {
	Kind   string `json:"kind"`
	Parent string `json:"parent"`
}

func (s *Server) handleFloatingIPAttach(w http.ResponseWriter, r *http.Request) {
	var body floatingIPAttachBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ref, err := s.resolveInstance(r.Context(), body.Parent)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found: "+body.Parent)
		return
	}
	f, ok, err := s.fips.SetAttachment(r.PathValue("floatingIp"), ref.vmid, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "floating IP not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.floatingToOxide(f))
}

func (s *Server) handleFloatingIPDetach(w http.ResponseWriter, r *http.Request) {
	f, ok, err := s.fips.SetAttachment(r.PathValue("floatingIp"), 0, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "floating IP not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.floatingToOxide(f))
}

// handleInstanceEphemeralAttach allocates an ephemeral external IP from a pool
// and DNATs it to the instance (POST /v1/instances/{instance}/external-ips/ephemeral).
// Ephemeral IPs are instance-bound and auto-released when the instance is deleted.
func (s *Server) handleInstanceEphemeralAttach(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.ensurePools()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	// The DNAT target is the instance's private SDN address; without one (e.g. a
	// flat-LAN VM) an ephemeral IP can't be routed back through takahe.
	if s.instancePrivateIP(ctx, ref.vmid) == "" {
		oxide.WriteError(w, http.StatusBadRequest,
			"ephemeral IPs require the instance to be on a VPC (SDN) subnet with an allocated address")
		return
	}
	var body struct {
		Pool string `json:"pool"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	pools, err := s.ippools.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var pool store.IPPool
	if body.Pool != "" {
		p, ok, _ := s.ippools.Get(body.Pool)
		if !ok {
			oxide.WriteError(w, http.StatusBadRequest, "ip pool not found: "+body.Pool)
			return
		}
		pool = p
	} else if p, ok := defaultUnicastV4Pool(pools); ok {
		pool = p
	} else {
		oxide.WriteError(w, http.StatusBadRequest, "no default IP pool available")
		return
	}

	existing, _ := s.fips.List()
	for _, f := range existing {
		if f.Ephemeral && f.InstanceVMID == ref.vmid {
			oxide.WriteError(w, http.StatusBadRequest, "instance already has an ephemeral IP")
			return
		}
	}
	now := time.Now().UTC()
	projectID := translate.ProjectID
	if ref.res.Pool != "" {
		projectID = translate.ProjectIDForPool(ref.res.Pool)
	}
	rec, err := s.fips.CreateAllocated(store.FloatingIP{
		ID:           translate.UUIDv5("ephemeral-ip:" + strconv.Itoa(ref.vmid)),
		Name:         "eph-" + strconv.Itoa(ref.vmid),
		Description:  "ephemeral external IP",
		PoolID:       pool.ID,
		ProjectID:    projectID,
		InstanceVMID: ref.vmid,
		Ephemeral:    true,
		TimeCreated:  now,
		TimeModified: now,
	}, func(used map[string]bool) string { return allocateFromPool(pool, used) })
	if err != nil {
		if err == store.ErrNameTaken || err == store.ErrIPTaken || err == store.ErrNoFreeIP {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, map[string]any{
		"kind": "ephemeral", "ip": rec.IP, "ip_pool_id": pool.ID,
	})
}

// handleInstanceEphemeralDetach releases the instance's ephemeral external IP.
func (s *Server) handleInstanceEphemeralDetach(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	fips, _ := s.fips.List()
	for _, f := range fips {
		if f.Ephemeral && f.InstanceVMID == ref.vmid {
			if _, derr := s.fips.Delete(f.ID); derr != nil {
				oxide.WriteError(w, http.StatusInternalServerError, derr.Error())
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "no ephemeral IP attached")
}

// releaseInstanceFloatingIPs drops ephemeral IPs bound to a deleted instance and
// detaches any floating IPs that were attached to it.
func (s *Server) releaseInstanceFloatingIPs(vmid int) {
	fips, err := s.fips.List()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, f := range fips {
		if f.InstanceVMID != vmid {
			continue
		}
		if f.Ephemeral {
			_, _ = s.fips.Delete(f.ID)
		} else {
			_, _, _ = s.fips.SetAttachment(f.ID, 0, now) // keep the floating IP, detach it
		}
	}
}

// handleFloatingIPMap is the unauthenticated (token-gated) endpoint the takahe
// reconciler polls. It returns the active floating_ip -> target private IP
// mappings, resolving each attached instance's current SDN address (independent
// of the floating IP's own block, so the external pool and the private VM subnet
// can be disjunct).
func (s *Server) handleFloatingIPMap(w http.ResponseWriter, r *http.Request) {
	if s.cfg.InternalToken != "" && r.Header.Get("X-Oxidize-Token") != s.cfg.InternalToken {
		oxide.WriteError(w, http.StatusUnauthorized, "invalid internal token")
		return
	}
	ctx := r.Context()
	// The reconciler polls this on a timer; fetch the cluster-wide lookups
	// (VM list, SDN IPAM, subnet CIDRs) once and resolve every target against
	// them rather than per attached resource.
	rc := s.newIPResolveCtx(ctx)
	// Floating IPs -> DNAT entries (opaque mapping).
	type dnatEntry struct {
		FloatingIP string `json:"floating_ip"`
		Target     string `json:"target"`
	}
	dnat := []dnatEntry{}
	if fips, err := s.fips.List(); err == nil {
		for _, f := range fips {
			if f.InstanceVMID == 0 {
				continue
			}
			if target := s.instancePrivateIPWith(ctx, f.InstanceVMID, rc); target != "" {
				dnat = append(dnat, dnatEntry{FloatingIP: f.IP, Target: target})
			}
		}
	}
	// External subnets -> routed CIDRs (non-opaque, routed to the instance).
	type routeEntry struct {
		CIDR string `json:"cidr"`
		Via  string `json:"via"`
	}
	routes := []routeEntry{}
	if exts, err := s.extsubnets.List(); err == nil {
		for _, e := range exts {
			if e.InstanceVMID == 0 {
				continue
			}
			if via := s.instancePrivateIPWith(ctx, e.InstanceVMID, rc); via != "" {
				routes = append(routes, routeEntry{CIDR: e.Subnet, Via: via})
			}
		}
	}
	oxide.WriteJSON(w, http.StatusOK, map[string]any{"dnat": dnat, "routes": routes})
}

// ipResolveCtx caches the cluster-wide lookups instancePrivateIPWith needs (the
// VM list, SDN IPAM allocations, and SDN subnet CIDRs), so a caller resolving
// many instances fetches each only once instead of per VM.
type ipResolveCtx struct {
	vms  []proxmox.ClusterResource
	ipam []proxmox.SDNIPAMEntry
	nets []*net.IPNet // SDN subnet CIDRs, for the guest-agent fallback
}

// newIPResolveCtx fetches the shared lookups once. Failures degrade to empty
// slices (the resolver then simply finds no address), matching the fail-soft
// behavior elsewhere.
func (s *Server) newIPResolveCtx(ctx context.Context) *ipResolveCtx {
	rc := &ipResolveCtx{}
	rc.vms, _ = s.listVMs(ctx)
	rc.ipam, _ = s.pve.SDNIPAMStatus(ctx)
	for _, subs := range s.sdnTopology(ctx).subnets {
		for _, sub := range subs {
			if _, n, err := net.ParseCIDR(sub.CIDR); err == nil {
				rc.nets = append(rc.nets, n)
			}
		}
	}
	return rc
}

// instancePrivateIP returns the VM's private SDN (VPC-subnet) address — the
// address takahe should DNAT a floating IP to, or route an external subnet via.
// This convenience form fetches the shared lookups for a single resolution; the
// map endpoint uses instancePrivateIPWith to share them across many instances.
func (s *Server) instancePrivateIP(ctx context.Context, vmid int) string {
	return s.instancePrivateIPWith(ctx, vmid, s.newIPResolveCtx(ctx))
}

// instancePrivateIPWith resolves a VM's private SDN address using pre-fetched
// cluster lookups. It prefers Proxmox SDN IPAM (which works without a guest
// agent and for stopped VMs, since the allocation persists) and falls back to a
// guest-agent IPv4 that lands inside an SDN subnet. Independent of any floating
// IP's own block, so the external pool and the private VM subnet can be disjunct.
func (s *Server) instancePrivateIPWith(ctx context.Context, vmid int, rc *ipResolveCtx) string {
	node := ""
	for _, v := range rc.vms {
		if v.VMID == vmid {
			node = v.Node
			break
		}
	}
	if node == "" {
		return ""
	}
	cfg, _ := s.pve.QemuConfig(ctx, node, vmid)

	// 1) Proxmox SDN IPAM by MAC.
	macs := map[string]bool{}
	for i := 0; i < 32; i++ {
		dev := "net" + strconv.Itoa(i)
		if v, ok := cfg[dev]; ok {
			if mac := translate.NICFromConfig(vmid, dev, v, nil, nil).MAC; mac != "" {
				macs[mac] = true
			}
		}
	}
	for _, e := range rc.ipam {
		if e.MAC != "" && macs[strings.ToUpper(e.MAC)] {
			return e.IP
		}
	}

	// 2) Fallback: a guest-agent IPv4 within an SDN subnet.
	for _, ip := range s.pve.AgentIPv4s(ctx, node, vmid) {
		pip := net.ParseIP(ip)
		for _, n := range rc.nets {
			if n.Contains(pip) {
				return ip
			}
		}
	}
	return ""
}

// --- IPv4 range helpers ---

func ipToU32(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3]), true
}

func u32ToIP(u uint32) string {
	return net.IPv4(byte(u>>24), byte(u>>16), byte(u>>8), byte(u)).String()
}

func parseIPRange(s string) (start, end uint32, ok bool) {
	a, b, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, false
	}
	sa, ok1 := ipToU32(net.ParseIP(strings.TrimSpace(a)))
	sb, ok2 := ipToU32(net.ParseIP(strings.TrimSpace(b)))
	if !ok1 || !ok2 || sa > sb {
		return 0, 0, false
	}
	return sa, sb, true
}

func firstFreeIP(start, end uint32, used map[string]bool) string {
	for u := start; u <= end; u++ {
		ip := u32ToIP(u)
		if !used[ip] {
			return ip
		}
	}
	return ""
}

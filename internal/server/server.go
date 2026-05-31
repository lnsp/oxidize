// Package server wires the Oxide-compatible HTTP API (translated to Proxmox)
// together with the embedded Console UI.
package server

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lnsp/oxidize/internal/config"
	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/static"
	"github.com/lnsp/oxidize/internal/store"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg         config.Config
	pve         *proxmox.Client
	keys        *store.SSHKeyStore
	fips        *store.FloatingIPStore
	ippools     *store.IPPoolStore
	subnetpools *store.SubnetPoolStore
	extsubnets  *store.ExternalSubnetStore
	affgroups   *store.AffinityGroupStore

	// SDN topology cache (see sdnTopology); guarded by sdnMu.
	sdnMu     sync.Mutex
	sdnCache  *sdnTopo
	sdnExpiry time.Time
}

// New builds a Server.
func New(cfg config.Config, pve *proxmox.Client, keys *store.SSHKeyStore, fips *store.FloatingIPStore, ippools *store.IPPoolStore, subnetpools *store.SubnetPoolStore, extsubnets *store.ExternalSubnetStore, affgroups *store.AffinityGroupStore) *Server {
	return &Server{cfg: cfg, pve: pve, keys: keys, fips: fips, ippools: ippools, subnetpools: subnetpools, extsubnets: extsubnets, affgroups: affgroups}
}

// Handler returns the fully wired http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- Auth (unauthenticated) ---
	mux.HandleFunc("POST /v1/login/{silo}/local", s.handleLogin)
	mux.HandleFunc("POST /v1/logout", s.handleLogout)

	// --- Current user ---
	mux.HandleFunc("GET /v1/me", s.protected(s.handleMe))
	mux.HandleFunc("GET /v1/me/groups", s.protected(s.handleMeGroups))
	mux.HandleFunc("GET /v1/me/ssh-keys", s.protected(s.handleSshKeyList))
	mux.HandleFunc("POST /v1/me/ssh-keys", s.protected(s.handleSshKeyCreate))
	mux.HandleFunc("GET /v1/me/ssh-keys/{sshKey}", s.protected(s.handleSshKeyView))
	mux.HandleFunc("DELETE /v1/me/ssh-keys/{sshKey}", s.protected(s.handleSshKeyDelete))

	// --- Projects (single synthetic project) ---
	mux.HandleFunc("GET /v1/projects", s.protected(s.handleProjectList))
	mux.HandleFunc("POST /v1/projects", s.protected(s.handleProjectCreate))
	mux.HandleFunc("GET /v1/projects/{project}", s.protected(s.handleProjectView))
	mux.HandleFunc("PUT /v1/projects/{project}", s.protected(s.handleProjectUpdate))
	mux.HandleFunc("DELETE /v1/projects/{project}", s.protected(s.handleProjectDelete))

	// --- Instances ---
	mux.HandleFunc("GET /v1/instances", s.protected(s.handleInstanceList))
	mux.HandleFunc("POST /v1/instances", s.protected(s.handleInstanceCreate))
	mux.HandleFunc("GET /v1/instances/{instance}", s.protected(s.handleInstanceView))
	mux.HandleFunc("PUT /v1/instances/{instance}", s.protected(s.handleInstanceUpdate))
	mux.HandleFunc("POST /v1/instances/{instance}/start", s.protected(s.instanceAction("start")))
	mux.HandleFunc("POST /v1/instances/{instance}/stop", s.protected(s.instanceAction("shutdown")))
	mux.HandleFunc("POST /v1/instances/{instance}/reboot", s.protected(s.instanceAction("reboot")))
	mux.HandleFunc("DELETE /v1/instances/{instance}", s.protected(s.handleInstanceDelete))
	mux.HandleFunc("GET /v1/instances/{instance}/disks", s.protected(s.handleInstanceDisks))
	mux.HandleFunc("POST /v1/instances/{instance}/disks/attach", s.protected(s.handleDiskAttach))
	mux.HandleFunc("POST /v1/instances/{instance}/disks/detach", s.protected(s.handleDiskDetach))
	mux.HandleFunc("GET /v1/instances/{instance}/external-ips", s.protected(s.handleInstanceExternalIPList))
	mux.HandleFunc("POST /v1/instances/{instance}/external-ips/ephemeral", s.protected(s.handleInstanceEphemeralAttach))
	mux.HandleFunc("DELETE /v1/instances/{instance}/external-ips/ephemeral", s.protected(s.handleInstanceEphemeralDetach))
	mux.HandleFunc("GET /v1/instances/{instance}/ssh-public-keys", s.protected(s.emptyPage))
	mux.HandleFunc("GET /v1/instances/{instance}/affinity-groups", s.protected(s.affinityGroups().instanceGroupList))
	mux.HandleFunc("GET /v1/instances/{instance}/anti-affinity-groups", s.protected(s.antiAffinityGroups().instanceGroupList))

	// --- Network interfaces, VPC, IP pools (mapped/synthetic) ---
	mux.HandleFunc("GET /v1/network-interfaces", s.protected(s.handleNICList))
	mux.HandleFunc("POST /v1/network-interfaces", s.protected(s.handleNICCreate))
	mux.HandleFunc("PUT /v1/network-interfaces/{interface}", s.protected(s.handleNICUpdate))
	mux.HandleFunc("DELETE /v1/network-interfaces/{interface}", s.protected(s.handleNICDelete))
	mux.HandleFunc("GET /v1/vpcs", s.protected(s.handleVpcList))
	mux.HandleFunc("GET /v1/vpcs/{vpc}", s.protected(s.handleVpcView))
	mux.HandleFunc("GET /v1/vpc-subnets", s.protected(s.handleVpcSubnetList))
	mux.HandleFunc("POST /v1/vpc-subnets", s.protected(s.handleVpcSubnetCreate))
	mux.HandleFunc("GET /v1/vpc-subnets/{subnet}", s.protected(s.handleVpcSubnetView))
	mux.HandleFunc("DELETE /v1/vpc-subnets/{subnet}", s.protected(s.handleVpcSubnetDelete))
	mux.HandleFunc("GET /v1/vpc-firewall-rules", s.protected(s.handleFirewallRules))
	mux.HandleFunc("GET /v1/ip-pools", s.protected(s.handleIPPoolList))
	mux.HandleFunc("GET /v1/ip-pools/{pool}", s.protected(s.handleIPPoolView))

	// --- Floating IPs (reserved SDN addresses, DNAT'd by takahe) ---
	mux.HandleFunc("GET /v1/floating-ips", s.protected(s.handleFloatingIPList))
	mux.HandleFunc("POST /v1/floating-ips", s.protected(s.handleFloatingIPCreate))
	mux.HandleFunc("GET /v1/floating-ips/{floatingIp}", s.protected(s.handleFloatingIPView))
	mux.HandleFunc("DELETE /v1/floating-ips/{floatingIp}", s.protected(s.handleFloatingIPDelete))
	mux.HandleFunc("POST /v1/floating-ips/{floatingIp}/attach", s.protected(s.handleFloatingIPAttach))
	mux.HandleFunc("POST /v1/floating-ips/{floatingIp}/detach", s.protected(s.handleFloatingIPDetach))

	// --- External subnets (routed CIDR blocks allocated from subnet pools) ---
	mux.HandleFunc("GET /v1/external-subnets", s.protected(s.handleExternalSubnetList))
	mux.HandleFunc("POST /v1/external-subnets", s.protected(s.handleExternalSubnetCreate))
	mux.HandleFunc("GET /v1/external-subnets/{externalSubnet}", s.protected(s.handleExternalSubnetView))
	mux.HandleFunc("DELETE /v1/external-subnets/{externalSubnet}", s.protected(s.handleExternalSubnetDelete))
	mux.HandleFunc("POST /v1/external-subnets/{externalSubnet}/attach", s.protected(s.handleExternalSubnetAttach))
	mux.HandleFunc("POST /v1/external-subnets/{externalSubnet}/detach", s.protected(s.handleExternalSubnetDetach))

	// --- Affinity / anti-affinity groups (oxidize-owned; recorded, not enforced) ---
	aff := s.affinityGroups()
	mux.HandleFunc("GET /v1/affinity-groups", s.protected(aff.list))
	mux.HandleFunc("POST /v1/affinity-groups", s.protected(aff.create))
	mux.HandleFunc("GET /v1/affinity-groups/{group}", s.protected(aff.view))
	mux.HandleFunc("PUT /v1/affinity-groups/{group}", s.protected(aff.update))
	mux.HandleFunc("DELETE /v1/affinity-groups/{group}", s.protected(aff.delete))
	mux.HandleFunc("GET /v1/affinity-groups/{group}/members", s.protected(aff.memberList))
	mux.HandleFunc("GET /v1/affinity-groups/{group}/members/instance/{instance}", s.protected(aff.memberView))
	mux.HandleFunc("POST /v1/affinity-groups/{group}/members/instance/{instance}", s.protected(aff.memberAdd))
	mux.HandleFunc("DELETE /v1/affinity-groups/{group}/members/instance/{instance}", s.protected(aff.memberDelete))

	anti := s.antiAffinityGroups()
	mux.HandleFunc("GET /v1/anti-affinity-groups", s.protected(anti.list))
	mux.HandleFunc("POST /v1/anti-affinity-groups", s.protected(anti.create))
	mux.HandleFunc("GET /v1/anti-affinity-groups/{group}", s.protected(anti.view))
	mux.HandleFunc("PUT /v1/anti-affinity-groups/{group}", s.protected(anti.update))
	mux.HandleFunc("DELETE /v1/anti-affinity-groups/{group}", s.protected(anti.delete))
	mux.HandleFunc("GET /v1/anti-affinity-groups/{group}/members", s.protected(anti.memberList))
	mux.HandleFunc("GET /v1/anti-affinity-groups/{group}/members/instance/{instance}", s.protected(anti.memberView))
	mux.HandleFunc("POST /v1/anti-affinity-groups/{group}/members/instance/{instance}", s.protected(anti.memberAdd))
	mux.HandleFunc("DELETE /v1/anti-affinity-groups/{group}/members/instance/{instance}", s.protected(anti.memberDelete))

	// --- Internal: floating IP -> instance map polled by the takahe reconciler.
	// Token-gated (X-Oxidize-Token), not session-protected (no browser session).
	mux.HandleFunc("GET /internal/floating-ip-map", s.handleFloatingIPMap)

	// --- Serial console ---
	mux.HandleFunc("GET /v1/instances/{instance}/serial-console", s.protected(s.handleSerialHistory))
	mux.HandleFunc("GET /v1/instances/{instance}/serial-console/stream", s.handleSerialStream)

	// --- Disks & images ---
	mux.HandleFunc("GET /v1/disks", s.protected(s.handleDiskList))
	mux.HandleFunc("POST /v1/disks", s.protected(s.handleDiskCreate))
	mux.HandleFunc("GET /v1/disks/{disk}", s.protected(s.handleDiskView))
	mux.HandleFunc("DELETE /v1/disks/{disk}", s.protected(s.handleDiskDelete))
	mux.HandleFunc("GET /v1/images", s.protected(s.handleImageList))
	mux.HandleFunc("GET /v1/images/{image}", s.protected(s.handleImageView))
	mux.HandleFunc("DELETE /v1/images/{image}", s.protected(s.handleImageDelete))

	// --- Snapshots (Proxmox VM snapshots) ---
	mux.HandleFunc("GET /v1/snapshots", s.protected(s.handleSnapshotList))
	mux.HandleFunc("POST /v1/snapshots", s.protected(s.handleSnapshotCreate))
	mux.HandleFunc("GET /v1/snapshots/{snapshot}", s.protected(s.handleSnapshotView))
	mux.HandleFunc("DELETE /v1/snapshots/{snapshot}", s.protected(s.handleSnapshotDelete))

	// --- Metrics (OxQL timeseries) ---
	mux.HandleFunc("POST /v1/timeseries/query", s.protected(s.handleTimeseriesQuery))
	mux.HandleFunc("POST /v1/system/timeseries/query", s.protected(s.handleTimeseriesQuery))
	mux.HandleFunc("GET /v1/system/metrics/{metricName}", s.protected(s.handleSystemMetric))
	mux.HandleFunc("GET /v1/metrics/{metricName}", s.protected(s.handleSystemMetric))

	// --- System: hardware, silos, utilization ---
	mux.HandleFunc("GET /v1/system/hardware/racks", s.protected(s.handleRackList))
	mux.HandleFunc("GET /v1/system/hardware/sleds", s.protected(s.handleSledList))
	mux.HandleFunc("GET /v1/system/hardware/sleds/{sledId}", s.protected(s.handleSledView))
	mux.HandleFunc("GET /v1/system/hardware/sleds/{sledId}/instances", s.protected(s.handleSledInstanceList))
	mux.HandleFunc("GET /v1/system/hardware/disks", s.protected(s.handlePhysicalDiskList))
	mux.HandleFunc("GET /v1/system/silos", s.protected(s.handleSiloList))
	mux.HandleFunc("GET /v1/system/silos/{silo}", s.protected(s.handleSiloView))
	mux.HandleFunc("GET /v1/utilization", s.protected(s.handleUtilization))
	mux.HandleFunc("GET /v1/system/utilization/silos", s.protected(s.handleSiloUtilizationList))

	// --- System: IP pool administration (System -> Networking -> IP Pools) ---
	mux.HandleFunc("GET /v1/system/ip-pools", s.protected(s.handleSystemIPPoolList))
	mux.HandleFunc("POST /v1/system/ip-pools", s.protected(s.handleSystemIPPoolCreate))
	mux.HandleFunc("GET /v1/system/ip-pools/{pool}", s.protected(s.handleSystemIPPoolView))
	mux.HandleFunc("PUT /v1/system/ip-pools/{pool}", s.protected(s.handleSystemIPPoolUpdate))
	mux.HandleFunc("DELETE /v1/system/ip-pools/{pool}", s.protected(s.handleSystemIPPoolDelete))
	mux.HandleFunc("GET /v1/system/ip-pools/{pool}/ranges", s.protected(s.handleSystemIPPoolRangeList))
	mux.HandleFunc("POST /v1/system/ip-pools/{pool}/ranges/add", s.protected(s.handleSystemIPPoolRangeAdd))
	mux.HandleFunc("POST /v1/system/ip-pools/{pool}/ranges/remove", s.protected(s.handleSystemIPPoolRangeRemove))
	mux.HandleFunc("GET /v1/system/ip-pools/{pool}/silos", s.protected(s.handleSystemIPPoolSiloList))
	mux.HandleFunc("POST /v1/system/ip-pools/{pool}/silos", s.protected(s.handleSystemIPPoolSiloLink))
	mux.HandleFunc("PUT /v1/system/ip-pools/{pool}/silos/{silo}", s.protected(s.handleSystemIPPoolSiloUpdate))
	mux.HandleFunc("DELETE /v1/system/ip-pools/{pool}/silos/{silo}", s.protected(s.handleSystemIPPoolSiloUnlink))
	mux.HandleFunc("GET /v1/system/ip-pools/{pool}/utilization", s.protected(s.handleSystemIPPoolUtilization))

	// --- Subnet pools (silo-scoped + System -> Networking -> Subnet Pools) ---
	mux.HandleFunc("GET /v1/subnet-pools", s.protected(s.handleSiloSubnetPoolList))
	mux.HandleFunc("GET /v1/system/subnet-pools", s.protected(s.handleSystemSubnetPoolList))
	mux.HandleFunc("POST /v1/system/subnet-pools", s.protected(s.handleSystemSubnetPoolCreate))
	mux.HandleFunc("GET /v1/system/subnet-pools/{pool}", s.protected(s.handleSystemSubnetPoolView))
	mux.HandleFunc("PUT /v1/system/subnet-pools/{pool}", s.protected(s.handleSystemSubnetPoolUpdate))
	mux.HandleFunc("DELETE /v1/system/subnet-pools/{pool}", s.protected(s.handleSystemSubnetPoolDelete))
	mux.HandleFunc("GET /v1/system/subnet-pools/{pool}/members", s.protected(s.handleSystemSubnetPoolMemberList))
	mux.HandleFunc("POST /v1/system/subnet-pools/{pool}/members/add", s.protected(s.handleSystemSubnetPoolMemberAdd))
	mux.HandleFunc("POST /v1/system/subnet-pools/{pool}/members/remove", s.protected(s.handleSystemSubnetPoolMemberRemove))
	mux.HandleFunc("GET /v1/system/subnet-pools/{pool}/silos", s.protected(s.handleSystemSubnetPoolSiloList))
	mux.HandleFunc("POST /v1/system/subnet-pools/{pool}/silos", s.protected(s.handleSystemSubnetPoolSiloLink))
	mux.HandleFunc("PUT /v1/system/subnet-pools/{pool}/silos/{silo}", s.protected(s.handleSystemSubnetPoolSiloUpdate))
	mux.HandleFunc("DELETE /v1/system/subnet-pools/{pool}/silos/{silo}", s.protected(s.handleSystemSubnetPoolSiloUnlink))
	mux.HandleFunc("GET /v1/system/subnet-pools/{pool}/utilization", s.protected(s.handleSystemSubnetPoolUtilization))

	// --- System update (read-only: shows the running Proxmox VE version) ---
	mux.HandleFunc("GET /v1/system/update/status", s.protected(s.handleUpdateStatus))
	mux.HandleFunc("GET /v1/system/update/repositories", s.protected(s.handleUpdateRepositoryList))
	mux.HandleFunc("PUT /v1/system/update/target-release", s.protected(s.handleTargetReleaseUpdate))

	// --- Empty stubs for resource pages we don't map yet ---
	for _, p := range emptyListRoutes {
		mux.HandleFunc("GET "+p, s.protected(s.emptyPage))
	}

	// --- Catch-all for any other /v1 path: JSON 404 (never leak HTML) ---
	mux.HandleFunc("/v1/", s.protected(s.handleNotFound))

	// --- /login redirect ---
	// The console's navToLogin sends users to bare /login, but the only SPA
	// route is /login/{silo}/local. In a real Oxide deployment the silo comes
	// from the subdomain and the server redirects; we do the same to our single
	// synthetic silo, preserving any redirect_uri.
	mux.HandleFunc("GET /login", s.handleLoginRedirect)

	// --- Static UI (SPA fallback) ---
	mux.Handle("/", static.Handler())

	return s.middleware(mux)
}

// emptyListRoutes are list endpoints rendered as empty pages so their UI pages
// load without errors. They're not (yet) mapped to Proxmox concepts.
var emptyListRoutes = []string{
	"/v1/vpc-routers",
	"/v1/vpc-router-routes",
	"/v1/internet-gateways",
	"/v1/system/networking/address-lot",
	"/v1/system/networking/loopback-address",
}

// middleware applies cross-cutting concerns: panic recovery and request logging.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic handling %s %s: %v", r.Method, r.URL.Path, rec)
				oxide.WriteError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// handleNotFound is the /v1/* catch-all for endpoints we don't explicitly map.
// Unmapped GETs return an empty results page so the console's list-prefetching
// loaders degrade gracefully (an empty table) instead of throwing on a 404 and
// rendering "Page not found". Writes still 404 — silently succeeding would be
// misleading.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		log.Printf("unmapped GET %s -> empty page", r.URL.Path)
		oxide.WriteJSON(w, http.StatusOK, oxide.Page([]any{}))
		return
	}
	oxide.WriteError(w, http.StatusNotFound, "not found: "+r.URL.Path)
}

// emptyPage responds with an empty results page.
func (s *Server) emptyPage(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]any{}))
}

// pveTimeout is the bound for blocking on a Proxmox task before returning.
const pveTimeout = 10 * time.Second

// cloneTimeout bounds waiting on a template clone, which can take longer than a
// simple lifecycle action (especially a full clone that copies the disk).
const cloneTimeout = 90 * time.Second

// Package server wires the Oxide-compatible HTTP API (translated to Proxmox)
// together with the embedded Console UI.
package server

import (
	"log"
	"net/http"
	"time"

	"github.com/lnsp/oxidize/internal/config"
	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/static"
	"github.com/lnsp/oxidize/internal/store"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg  config.Config
	pve  *proxmox.Client
	keys *store.SSHKeyStore
}

// New builds a Server.
func New(cfg config.Config, pve *proxmox.Client, keys *store.SSHKeyStore) *Server {
	return &Server{cfg: cfg, pve: pve, keys: keys}
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
	mux.HandleFunc("GET /v1/projects/{project}", s.protected(s.handleProjectView))

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
	mux.HandleFunc("GET /v1/instances/{instance}/ssh-public-keys", s.protected(s.emptyPage))
	mux.HandleFunc("GET /v1/instances/{instance}/affinity-groups", s.protected(s.emptyPage))
	mux.HandleFunc("GET /v1/instances/{instance}/anti-affinity-groups", s.protected(s.emptyPage))

	// --- Network interfaces, VPC, IP pools (mapped/synthetic) ---
	mux.HandleFunc("GET /v1/network-interfaces", s.protected(s.handleNICList))
	mux.HandleFunc("POST /v1/network-interfaces", s.protected(s.handleNICCreate))
	mux.HandleFunc("PUT /v1/network-interfaces/{interface}", s.protected(s.handleNICUpdate))
	mux.HandleFunc("DELETE /v1/network-interfaces/{interface}", s.protected(s.handleNICDelete))
	mux.HandleFunc("GET /v1/vpcs", s.protected(s.handleVpcList))
	mux.HandleFunc("GET /v1/vpcs/{vpc}", s.protected(s.handleVpcView))
	mux.HandleFunc("GET /v1/vpc-subnets", s.protected(s.handleVpcSubnetList))
	mux.HandleFunc("GET /v1/vpc-subnets/{subnet}", s.protected(s.handleVpcSubnetView))
	mux.HandleFunc("GET /v1/vpc-firewall-rules", s.protected(s.handleFirewallRules))
	mux.HandleFunc("GET /v1/ip-pools", s.protected(s.handleIPPoolList))
	mux.HandleFunc("GET /v1/ip-pools/{pool}", s.protected(s.handleIPPoolView))

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
	"/v1/floating-ips",
	"/v1/internet-gateways",
	"/v1/affinity-groups",
	"/v1/anti-affinity-groups",
	"/v1/system/ip-pools",
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

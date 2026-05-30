package server

import (
	"net/http"

	"github.com/lnsp/oxidize/internal/oxide"
)

// The console's System Update page is built around Oxide's rack-update model
// (a target release, components-by-version, and uploadable TUF repos). Proxmox
// uses per-node apt package updates, which don't fit that model and have no slot
// in this UI. So we present it read-only: the running Proxmox VE version is
// shown as the current/target "release". Driving apt upgrades is not supported
// here — use the Proxmox web UI or `apt` for that.

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	version := "unknown"
	if v, err := s.pve.Version(ctx); err == nil && v.Version != "" {
		version = v.Version
	}
	count := 1
	if nodes, err := s.pve.Nodes(ctx); err == nil && len(nodes) > 0 {
		count = len(nodes)
	}
	oxide.WriteJSON(w, http.StatusOK, map[string]any{
		"components_by_release_version": map[string]int{version: count},
		"contact_support":               false,
		"suspended":                     false,
		"target_release": map[string]any{
			"version":        version,
			"time_requested": epochTime(),
		},
		"time_last_step_planned": epochTime(),
	})
}

func (s *Server) handleUpdateRepositoryList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repos := []map[string]any{}
	if v, err := s.pve.Version(ctx); err == nil && v.Version != "" {
		repos = append(repos, map[string]any{
			"system_version": v.Version,
			"file_name":      "proxmox-ve",
			"hash":           v.RepoID,
			"time_created":   epochTime(),
		})
	}
	oxide.WriteJSON(w, http.StatusOK, map[string]any{"items": repos, "next_page": nil})
}

func (s *Server) handleTargetReleaseUpdate(w http.ResponseWriter, r *http.Request) {
	oxide.WriteError(w, http.StatusBadRequest,
		"setting a target release isn't supported on Proxmox; manage updates with apt or the Proxmox web UI")
}

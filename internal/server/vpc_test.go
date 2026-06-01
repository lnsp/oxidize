package server

import (
	"context"
	"testing"
	"time"

	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/store"
	"github.com/lnsp/oxidize/internal/translate"
)

func TestZoneNameFor(t *testing.T) {
	s := &Server{}
	// Collides with existing zones -> must pick a distinct, valid token.
	topo := sdnTopo{zones: []proxmox.SDNZone{{Zone: "web"}, {Zone: "web0"}}}
	n := s.zoneNameFor("web", topo)
	if n == "web" || n == "web0" {
		t.Errorf("zoneNameFor must avoid existing zones, got %q", n)
	}
	if len(n) > 8 {
		t.Errorf("zone name exceeds 8 chars: %q", n)
	}
	// A long name is truncated to the 8-char zone limit.
	if long := s.zoneNameFor("averylongvpcname", sdnTopo{}); len(long) > 8 {
		t.Errorf("long name not truncated: %q (%d)", long, len(long))
	}
}

// vpcsForProject shows the default VPC always, an oxidize-owned zone only in its
// owning project, and a legacy (un-owned) zone in every project.
func TestVpcsForProjectScoping(t *testing.T) {
	vs := store.NewVPCStore(t.TempDir())
	now := time.Now()
	if _, err := vs.Create(store.VPC{
		ID: translate.VPCIDForZone("zmine"), Zone: "zmine",
		ProjectID: translate.ProjectID, Name: "mine", DNSName: "mine",
		TimeCreated: now, TimeModified: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := vs.Create(store.VPC{
		ID: translate.VPCIDForZone("zother"), Zone: "zother",
		ProjectID: "other-project", Name: "other", DNSName: "other",
		TimeCreated: now, TimeModified: now,
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{vpcs: vs}
	topo := sdnTopo{zones: []proxmox.SDNZone{{Zone: "zmine"}, {Zone: "zother"}, {Zone: "lab"}}}

	// projectRef "" resolves to the default project (no Proxmox call).
	names := map[string]bool{}
	for _, v := range s.vpcsForProject(context.Background(), "", topo) {
		names[v.Name] = true
	}
	if !names["default"] {
		t.Error("default VPC should always be present")
	}
	if !names["mine"] {
		t.Error("VPC owned by this project should be present")
	}
	if names["other"] {
		t.Error("VPC owned by another project must be hidden")
	}
	if !names["lab"] {
		t.Error("legacy (un-owned) zone should be visible in every project")
	}
}

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/store"
	"github.com/lnsp/oxidize/internal/translate"
)

func TestCIDROverlaps(t *testing.T) {
	mk := func(s string) *net.IPNet { _, n, _ := net.ParseCIDR(s); return n }
	cases := []struct {
		a, b string
		want bool
	}{
		{"172.20.0.0/24", "172.20.0.0/24", true},   // identical
		{"172.20.0.0/24", "172.20.0.128/25", true}, // contained
		{"172.20.0.0/24", "172.20.10.0/24", false}, // disjoint
		{"172.20.0.0/16", "172.20.20.0/24", true},  // wider contains narrower
		{"10.0.0.0/24", "172.20.0.0/24", false},    // unrelated
	}
	for _, c := range cases {
		if got := cidrOverlaps(mk(c.a), mk(c.b)); got != c.want {
			t.Errorf("cidrOverlaps(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

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

// A VPC must resolve by id regardless of the requesting project (the console
// fetches a NIC's VPC by id with no ?project=). Regression test for a 404 that
// broke instance viewing when the instance's VPC was owned by another project.
func TestResolveVPCByIDIsGlobal(t *testing.T) {
	vs := store.NewVPCStore(t.TempDir())
	now := time.Now()
	if _, err := vs.Create(store.VPC{
		ID: translate.VPCIDForZone("test"), Zone: "test",
		ProjectID: "valar-project", Name: "test", DNSName: "test",
		TimeCreated: now, TimeModified: now,
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{vpcs: vs}
	// Empty project (resolves to the default project) must still find a VPC owned
	// by another project, looked up by id.
	v, ok := s.resolveVPC(context.Background(), translate.VPCIDForZone("test"), "")
	if !ok {
		t.Fatal("VPC owned by another project should resolve by id with empty project")
	}
	if v.ProjectID != "valar-project" {
		t.Errorf("resolved VPC project_id = %q, want the owner's", v.ProjectID)
	}
	// Also resolvable by name.
	if _, ok := s.resolveVPC(context.Background(), "test", ""); !ok {
		t.Error("VPC should resolve by name too")
	}
}

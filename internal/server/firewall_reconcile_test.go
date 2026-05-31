package server

import (
	"testing"

	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/translate"
)

// testTopo builds an SDN topology with one zone "lab" / vnet "lab0" (10.0.0.0/24)
// for the membership and ref-resolution tests.
func testTopo() sdnTopo {
	return sdnTopo{
		zones: []proxmox.SDNZone{{Zone: "lab"}},
		vnets: []proxmox.SDNVnet{{Vnet: "lab0", Zone: "lab"}},
		subnets: map[string][]proxmox.SDNSubnet{
			"lab0": {{Vnet: "lab0", CIDR: "10.0.0.0/24"}},
		},
	}
}

// A NIC on the SDN vnet classifies to that zone's VPC; a NIC on the flat LAN
// classifies to the default VPC. This is the core of vpcMembers.
func TestNICBridgeClassifiesToVPC(t *testing.T) {
	topo := testTopo()
	locate := topo.netLocator()
	labVPC := translate.VPCIDForZone("lab")

	cases := []struct {
		nic      string
		wantVPC  string
		wantName string
	}{
		{"virtio=AA:BB:CC:DD:EE:01,bridge=lab0,firewall=1", labVPC, "sdn vnet -> zone VPC"},
		{"virtio=AA:BB:CC:DD:EE:02,bridge=vmbr0", translate.VpcID, "flat LAN -> default VPC"},
		{"virtio=AA:BB:CC:DD:EE:03", translate.VpcID, "unspecified bridge -> default VPC (vmbr0)"},
	}
	for _, c := range cases {
		if got, _ := locate(translate.BridgeOf(c.nic)); got != c.wantVPC {
			t.Errorf("%s: bridge %q -> vpc %s, want %s", c.wantName, translate.BridgeOf(c.nic), got, c.wantVPC)
		}
	}
}

func TestSubnetCIDRByRef(t *testing.T) {
	topo := testTopo()
	want := "10.0.0.0/24"
	for _, ref := range []string{"lab0", translate.SubnetIDForVnet("lab0"), translate.SanitizeName("lab0", "vnet")} {
		if got := subnetCIDRByRef(ref, topo); got != want {
			t.Errorf("subnetCIDRByRef(%q) = %q, want %q", ref, got, want)
		}
	}
	if got := subnetCIDRByRef("nope", topo); got != "" {
		t.Errorf("unknown subnet ref should resolve to empty, got %q", got)
	}
}

func TestVPCIDByRef(t *testing.T) {
	topo := testTopo()
	want := translate.VPCIDForZone("lab")
	for _, ref := range []string{"lab", want, translate.SanitizeName("lab", "zone")} {
		if got := vpcIDByRef(ref, topo); got != want {
			t.Errorf("vpcIDByRef(%q) = %q, want %q", ref, got, want)
		}
	}
	if got := vpcIDByRef("nope", topo); got != "" {
		t.Errorf("unknown vpc ref should resolve to empty, got %q", got)
	}
}

func TestNICWithFirewall(t *testing.T) {
	cases := []struct {
		in          string
		want        string
		wantChanged bool
	}{
		{"virtio=AA:BB,bridge=lab0", "virtio=AA:BB,bridge=lab0,firewall=1", true},
		{"virtio=AA:BB,bridge=lab0,firewall=0", "virtio=AA:BB,bridge=lab0,firewall=1", true},
		{"virtio=AA:BB,bridge=lab0,firewall=1", "virtio=AA:BB,bridge=lab0,firewall=1", false},
	}
	for _, c := range cases {
		got, changed := nicWithFirewall(c.in)
		if got != c.want || changed != c.wantChanged {
			t.Errorf("nicWithFirewall(%q) = (%q,%v), want (%q,%v)", c.in, got, changed, c.want, c.wantChanged)
		}
	}
}

func TestOwnedVPCID(t *testing.T) {
	vpc := "abc-123"
	cases := []struct {
		comment string
		want    string
		ok      bool
	}{
		{translate.OwnerComment + vpc, vpc, true},
		{translate.OwnerComment + vpc + " allow-ssh", vpc, true},
		{"a user's own rule", "", false},
		{translate.OwnerComment, "", false},
	}
	for _, c := range cases {
		got, ok := ownedVPCID(c.comment)
		if got != c.want || ok != c.ok {
			t.Errorf("ownedVPCID(%q) = (%q,%v), want (%q,%v)", c.comment, got, ok, c.want, c.ok)
		}
	}
}

func TestFirewallRulesMatch(t *testing.T) {
	desired := []translate.PVEGroupRule{
		{Type: "in", Action: "ACCEPT", Proto: "tcp", Dport: "22", Source: "10.0.0.0/24", Enable: 1},
		{Type: "out", Action: "DROP", Proto: "udp", Dport: "53", Enable: 0},
	}
	same := []proxmox.FirewallRule{
		{Type: "in", Action: "ACCEPT", Proto: "tcp", Dport: "22", Source: "10.0.0.0/24", Enable: 1},
		{Type: "out", Action: "DROP", Proto: "udp", Dport: "53", Enable: 0},
	}
	if !firewallRulesMatch(same, desired) {
		t.Error("identical rule sets should match")
	}
	// Order matters.
	swapped := []proxmox.FirewallRule{same[1], same[0]}
	if firewallRulesMatch(swapped, desired) {
		t.Error("reordered rules should not match")
	}
	// A single field difference breaks the match.
	diff := append([]proxmox.FirewallRule(nil), same...)
	diff[0].Dport = "80"
	if firewallRulesMatch(diff, desired) {
		t.Error("differing dport should not match")
	}
	// Length mismatch.
	if firewallRulesMatch(same[:1], desired) {
		t.Error("length mismatch should not match")
	}
}

func TestGroupRuleFormOmitsEmpty(t *testing.T) {
	f := groupRuleForm(translate.PVEGroupRule{
		Type: "in", Action: "ACCEPT", Enable: 1, Proto: "tcp", Dport: "22",
		Source: "10.0.0.0/24", Comment: "oxidize:v allow-ssh",
	})
	if f.Get("type") != "in" || f.Get("action") != "ACCEPT" || f.Get("enable") != "1" {
		t.Fatalf("core fields wrong: %v", f)
	}
	if f.Get("proto") != "tcp" || f.Get("dport") != "22" || f.Get("source") != "10.0.0.0/24" {
		t.Fatalf("match fields wrong: %v", f)
	}
	// dest was empty -> must be omitted entirely (not sent as "").
	if _, ok := f["dest"]; ok {
		t.Error("empty dest should be omitted from the form")
	}
}

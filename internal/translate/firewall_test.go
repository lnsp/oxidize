package translate

import (
	"reflect"
	"testing"

	"github.com/lnsp/oxidize/internal/oxide"
)

// fakeResolver is a deterministic stand-in for the live address resolver: ip /
// ip_net references inline as literals, while vpc / subnet / instance references
// become an IPset whose name and members are derived from the ref, so tests can
// assert the full source/dest wiring without a cluster.
func fakeResolver(vpcID string) Resolver {
	return func(side []FirewallRef) ResolvedRef {
		if len(side) == 0 {
			return ResolvedRef{} // any
		}
		// One literal ip/ip_net ref inlines directly.
		if len(side) == 1 && (side[0].Type == "ip" || side[0].Type == "ip_net") {
			return ResolvedRef{Spec: side[0].Value}
		}
		// Otherwise synthesize an IPset from the (deterministic) members.
		var key string
		var members []string
		for _, r := range side {
			key += r.Type + "-" + r.Value + "_"
			members = append(members, "10.0.0."+r.Value)
		}
		name := FirewallIPSetName(vpcID, key)
		return ResolvedRef{Spec: "+" + name, IPSet: name, Members: members}
	}
}

func proto(t string) oxide.VpcFirewallRuleProtocol { return oxide.VpcFirewallRuleProtocol{Type: t} }

func TestFirewallGroupNameStableAndBounded(t *testing.T) {
	a := FirewallGroupName("vpc-1")
	b := FirewallGroupName("vpc-1")
	c := FirewallGroupName("vpc-2")
	if a != b {
		t.Fatalf("group name not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("distinct VPCs collided on group name: %q", a)
	}
	if len(a) > 18 {
		t.Fatalf("group name %q exceeds Proxmox's 18-char limit (%d)", a, len(a))
	}
}

func TestFirewallPlanInboundAllow(t *testing.T) {
	vpc := "vpc-1"
	rules := []oxide.VpcFirewallRule{{
		Name:      "allow-ssh",
		Action:    "allow",
		Direction: "inbound",
		Status:    "enabled",
		Priority:  100,
		Filters: oxide.VpcFirewallRuleFilter{
			Hosts:     []oxide.VpcFirewallRuleHostFilter{{Type: "ip_net", Value: "192.168.1.0/24"}},
			Ports:     []string{"22"},
			Protocols: []oxide.VpcFirewallRuleProtocol{proto("tcp")},
		},
		Targets: []oxide.VpcFirewallRuleTarget{{Type: "vpc", Value: "default"}},
	}}
	plan := BuildFirewallPlan(vpc, rules, fakeResolver(vpc))

	if plan.GroupName != FirewallGroupName(vpc) {
		t.Fatalf("group name = %q", plan.GroupName)
	}
	if len(plan.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(plan.Rules))
	}
	got := plan.Rules[0]
	// Inbound: source = host filter (the /24), dest = target (the VPC ipset).
	wantSet := FirewallIPSetName(vpc, "vpc-default_")
	want := PVEGroupRule{
		Pos: 0, Type: "in", Action: "ACCEPT", Enable: 1,
		Proto: "tcp", Dport: "22",
		Source: "192.168.1.0/24", Dest: "+" + wantSet,
		Comment: "oxidize:vpc-1 allow-ssh",
	}
	if got != want {
		t.Fatalf("rule mismatch:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(plan.IPSets[wantSet], []string{"10.0.0.default"}) {
		t.Fatalf("ipset members = %v", plan.IPSets[wantSet])
	}
}

func TestFirewallPlanOutboundSwapsSourceDest(t *testing.T) {
	vpc := "vpc-1"
	rules := []oxide.VpcFirewallRule{{
		Name: "deny-egress", Action: "deny", Direction: "outbound", Status: "enabled",
		Filters: oxide.VpcFirewallRuleFilter{
			Hosts:     []oxide.VpcFirewallRuleHostFilter{{Type: "ip", Value: "8.8.8.8"}},
			Protocols: []oxide.VpcFirewallRuleProtocol{proto("udp")},
			Ports:     []string{"53"},
		},
		Targets: []oxide.VpcFirewallRuleTarget{{Type: "instance", Value: "web"}},
	}}
	got := BuildFirewallPlan(vpc, rules, fakeResolver(vpc)).Rules[0]
	// Outbound: source = target (instance ipset), dest = host filter (literal).
	wantSet := FirewallIPSetName(vpc, "instance-web_")
	if got.Type != "out" || got.Action != "DROP" {
		t.Fatalf("type/action = %s/%s", got.Type, got.Action)
	}
	if got.Source != "+"+wantSet || got.Dest != "8.8.8.8" {
		t.Fatalf("source/dest = %q/%q (want +%s / 8.8.8.8)", got.Source, got.Dest, wantSet)
	}
	if got.Proto != "udp" || got.Dport != "53" {
		t.Fatalf("proto/dport = %q/%q", got.Proto, got.Dport)
	}
}

func TestFirewallPlanProtocolFanout(t *testing.T) {
	vpc := "vpc-1"
	rules := []oxide.VpcFirewallRule{{
		Name: "web", Action: "allow", Direction: "inbound", Status: "enabled",
		Filters: oxide.VpcFirewallRuleFilter{
			Protocols: []oxide.VpcFirewallRuleProtocol{proto("tcp"), proto("udp")},
			Ports:     []string{"80", "443", "8000-8100"},
		},
	}}
	plan := BuildFirewallPlan(vpc, rules, fakeResolver(vpc))
	if len(plan.Rules) != 2 {
		t.Fatalf("want 2 rules (one per protocol), got %d", len(plan.Rules))
	}
	if plan.Rules[0].Proto != "tcp" || plan.Rules[1].Proto != "udp" {
		t.Fatalf("protos = %q,%q", plan.Rules[0].Proto, plan.Rules[1].Proto)
	}
	// Port ranges: "-" becomes ":", list comma-joined; no host/target => any.
	if plan.Rules[0].Dport != "80,443,8000:8100" {
		t.Fatalf("dport = %q", plan.Rules[0].Dport)
	}
	if plan.Rules[0].Source != "" || plan.Rules[0].Dest != "" {
		t.Fatalf("expected any source/dest, got %q/%q", plan.Rules[0].Source, plan.Rules[0].Dest)
	}
	// Fanned-out rules keep distinct ascending positions.
	if plan.Rules[0].Pos != 0 || plan.Rules[1].Pos != 1 {
		t.Fatalf("positions = %d,%d", plan.Rules[0].Pos, plan.Rules[1].Pos)
	}
}

func TestFirewallPlanOrdersByPriorityAndDisables(t *testing.T) {
	vpc := "vpc-1"
	rules := []oxide.VpcFirewallRule{
		{Name: "low", Action: "allow", Direction: "inbound", Status: "enabled", Priority: 200},
		{Name: "high", Action: "deny", Direction: "inbound", Status: "disabled", Priority: 10},
	}
	plan := BuildFirewallPlan(vpc, rules, fakeResolver(vpc))
	if len(plan.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(plan.Rules))
	}
	// Lower priority number is ordered first.
	if plan.Rules[0].Comment != "oxidize:vpc-1 high" || plan.Rules[1].Comment != "oxidize:vpc-1 low" {
		t.Fatalf("order = %q, %q", plan.Rules[0].Comment, plan.Rules[1].Comment)
	}
	// Disabled rule still emitted, with enable=0.
	if plan.Rules[0].Enable != 0 {
		t.Fatalf("disabled rule should have enable=0, got %d", plan.Rules[0].Enable)
	}
	if plan.Rules[1].Enable != 1 {
		t.Fatalf("enabled rule should have enable=1, got %d", plan.Rules[1].Enable)
	}
}

func TestFirewallPlanEmpty(t *testing.T) {
	plan := BuildFirewallPlan("vpc-1", nil, fakeResolver("vpc-1"))
	if len(plan.Rules) != 0 {
		t.Fatalf("want no rules, got %d", len(plan.Rules))
	}
	if len(plan.IPSets) != 0 {
		t.Fatalf("want no ipsets, got %d", len(plan.IPSets))
	}
	if plan.GroupName == "" {
		t.Fatal("group name should be set even for an empty rule set")
	}
}

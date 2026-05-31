package translate

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
)

// This file is the PURE mapping from an Oxide VPC firewall rule set to the
// Proxmox objects that enforce it: a per-VPC security group of in/out rules
// plus the IPsets those rules reference. It performs NO I/O — address lookups
// (resolving a vpc/subnet/instance reference to concrete IPs) are delegated to
// a Resolver supplied by the caller, so the structural translation can be unit
// tested exhaustively without a live cluster. See
// docs/firewall-enforcement-plan.md for the design.

// FirewallRef is one side of a firewall match: a {type, value} reference where
// type is one of vpc | subnet | instance | ip | ip_net. It is the shared shape
// of both an Oxide rule target and a host filter.
type FirewallRef struct {
	Type  string
	Value string
}

// ResolvedRef is a Proxmox address spec for a rule side, plus any IPset it
// requires. Spec is what lands in a PVE rule's source/dest: a literal IP/CIDR
// ("10.0.0.4", "10.0.0.0/24"), an IPset reference ("+oxsetname"), or "" meaning
// "any" (no constraint). When Spec is an IPset reference, IPSet/Members declare
// the set the plan must create.
type ResolvedRef struct {
	Spec    string
	IPSet   string
	Members []string
}

// Resolver turns one side's references (a rule's targets, or its host filters)
// into a single Proxmox address spec. The caller owns the literal-vs-IPset
// decision and the live address lookup; the translation layer only wires the
// result into rules and collects the IPsets. A nil/empty side resolves to "any".
type Resolver func(side []FirewallRef) ResolvedRef

// PVEGroupRule is one rule inside a Proxmox security group, named for the PVE
// firewall API form fields it maps to. Pos is the intended order within the
// group (ascending), derived from Oxide rule priority.
type PVEGroupRule struct {
	Pos     int
	Type    string // "in" | "out"
	Action  string // "ACCEPT" | "DROP"
	Enable  int    // 0 | 1
	Proto   string // "tcp" | "udp" | "icmp" | "" (any)
	Dport   string // "80,443" | "8000:8100" | "" (any)
	Source  string // IP/CIDR | "+ipset" | "" (any)
	Dest    string // IP/CIDR | "+ipset" | "" (any)
	Comment string // "oxidize:<vpcID> <rule name>" — ownership tag
}

// FirewallPlan is the desired Proxmox state for one VPC's firewall: the security
// group name, its ordered rules, and the IPsets the rules reference (name ->
// member IPs/CIDRs).
type FirewallPlan struct {
	GroupName string
	Rules     []PVEGroupRule
	IPSets    map[string][]string
}

// OwnerComment is the prefix stamped on every Proxmox firewall object oxidize
// creates. The reconciler only ever deletes objects whose comment starts with
// OwnerComment + the VPC id, so a user's hand-written rules are never touched.
const OwnerComment = "oxidize:"

// FirewallGroupName derives the deterministic Proxmox security-group name for a
// VPC. Proxmox caps group names at 18 chars ([A-Za-z0-9-]); "oxidize" + 8 hex of
// sha1(vpcID) is 15, leaving headroom. Ownership is matched by the group's
// comment, not by parsing this name.
func FirewallGroupName(vpcID string) string {
	sum := sha1.Sum([]byte("fwgroup:" + vpcID))
	return "oxidize" + hex.EncodeToString(sum[:])[:8]
}

// FirewallIPSetName derives a deterministic Proxmox IPset name for a rule side
// within a VPC. key distinguishes sides/refs so distinct sets don't collide.
// IPset names are generously bounded, so the full prefix is safe.
func FirewallIPSetName(vpcID, key string) string {
	sum := sha1.Sum([]byte("fwipset:" + vpcID + ":" + key))
	return "ox" + hex.EncodeToString(sum[:])[:12]
}

// BuildFirewallPlan translates a VPC's Oxide rule set into the Proxmox security
// group + IPsets that enforce it. Rules are ordered by ascending Oxide priority
// (stable within equal priority). resolve maps each rule's targets and host
// filters to Proxmox address specs; the IPsets it returns are collected into the
// plan. A rule with multiple protocols fans out to one PVE rule per protocol.
func BuildFirewallPlan(vpcID string, rules []oxide.VpcFirewallRule, resolve Resolver) FirewallPlan {
	plan := FirewallPlan{
		GroupName: FirewallGroupName(vpcID),
		IPSets:    map[string][]string{},
	}

	ordered := make([]oxide.VpcFirewallRule, len(rules))
	copy(ordered, rules)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	pos := 0
	for _, r := range ordered {
		target := resolve(refsOf(r.Targets, func(t oxide.VpcFirewallRuleTarget) FirewallRef {
			return FirewallRef{Type: t.Type, Value: t.Value}
		}))
		host := resolve(refsOf(r.Filters.Hosts, func(h oxide.VpcFirewallRuleHostFilter) FirewallRef {
			return FirewallRef{Type: h.Type, Value: h.Value}
		}))
		collectIPSet(plan.IPSets, target)
		collectIPSet(plan.IPSets, host)

		// Inbound: traffic FROM the host filters TO the rule's targets.
		// Outbound: traffic FROM the targets TO the host filters.
		typ, source, dest := "in", host.Spec, target.Spec
		if r.Direction == "outbound" {
			typ, source, dest = "out", target.Spec, host.Spec
		}

		action := "DROP"
		if r.Action == "allow" {
			action = "ACCEPT"
		}
		enable := 1
		if r.Status == "disabled" {
			enable = 0
		}
		dport := pveDport(r.Filters.Ports)
		comment := OwnerComment + vpcID + " " + r.Name

		for _, proto := range pveProtos(r.Filters.Protocols) {
			plan.Rules = append(plan.Rules, PVEGroupRule{
				Pos:     pos,
				Type:    typ,
				Action:  action,
				Enable:  enable,
				Proto:   proto,
				Dport:   dport,
				Source:  source,
				Dest:    dest,
				Comment: comment,
			})
			pos++
		}
	}
	return plan
}

// refsOf adapts a slice of typed targets/host-filters into []FirewallRef.
func refsOf[T any](in []T, conv func(T) FirewallRef) []FirewallRef {
	out := make([]FirewallRef, 0, len(in))
	for _, v := range in {
		out = append(out, conv(v))
	}
	return out
}

// collectIPSet records a resolved side's IPset (if any) in the plan's set map.
func collectIPSet(sets map[string][]string, r ResolvedRef) {
	if r.IPSet != "" {
		sets[r.IPSet] = r.Members
	}
}

// pveProtos maps Oxide protocol filters to PVE proto values, one per protocol.
// An empty protocol list yields a single any-protocol rule ("").
func pveProtos(protos []oxide.VpcFirewallRuleProtocol) []string {
	if len(protos) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(protos))
	for _, p := range protos {
		// Oxide protocol type is "tcp" | "udp" | "icmp"; PVE uses the same
		// lowercase token. (ICMP type/code detail is not yet mapped.)
		out = append(out, strings.ToLower(p.Type))
	}
	return out
}

// pveDport converts Oxide port ranges to a PVE dport spec. Oxide ranges are
// "N" or "N-M"; PVE wants "N" or "N:M", comma-joined for a list.
func pveDport(ports []string) string {
	if len(ports) == 0 {
		return ""
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, strings.Replace(p, "-", ":", 1))
	}
	return strings.Join(out, ",")
}

package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

// This file makes VPC firewall rules real: it compiles a VPC's stored Oxide rule
// set (the desired state in s.fwrules) into a Proxmox cluster security group +
// IPsets and attaches the group to the VMs in the VPC. A periodic reconciler
// keeps the data plane in sync as VMs join/leave the VPC. See
// docs/firewall-enforcement-plan.md.
//
// Scope and safety (settled in the plan):
//   - SDN-backed VPCs only; the flat-LAN default VPC stays record-only.
//   - policy_in stays ACCEPT (no implicit default-deny) to avoid lockout.
//   - Every object oxidize writes is tagged with translate.OwnerComment +
//     "<vpcID>"; the reconciler only ever deletes objects bearing that tag, so a
//     user's hand-written Proxmox firewall rules are never touched.
//   - Gated by cfg.FirewallMode: "off" (record only), "dryrun" (log the intended
//     plan, write nothing), "on" (apply).

const (
	firewallModeOff    = "off"
	firewallModeDryRun = "dryrun"
	firewallModeOn     = "on"
)

func (s *Server) firewallMode() string {
	if s.cfg.FirewallMode == "" {
		return firewallModeOff
	}
	return s.cfg.FirewallMode
}

// StartFirewallReconciler runs the periodic reconcile loop until ctx is done.
// It is a no-op when FirewallMode is "off". Call it once at startup in a
// goroutine.
func (s *Server) StartFirewallReconciler(ctx context.Context) {
	if s.firewallMode() == firewallModeOff {
		return
	}
	interval := s.cfg.FirewallReconcileInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Printf("firewall reconciler started (mode=%s, interval=%s)", s.firewallMode(), interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	s.reconcileAllFirewall(ctx) // once at startup, don't wait a full interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reconcileAllFirewall(ctx)
		}
	}
}

// reconcileAllFirewall reconciles every SDN-backed VPC and sweeps orphaned
// firewall objects left by deleted zones.
func (s *Server) reconcileAllFirewall(ctx context.Context) {
	topo := s.sdnTopology(ctx)
	live := map[string]bool{}
	for _, z := range topo.zones {
		vpcID := translate.VPCIDForZone(z.Zone)
		live[vpcID] = true
		s.reconcileVPCFirewall(ctx, vpcID, topo)
	}
	s.sweepOrphanFirewall(ctx, live)
}

// reconcileVPCFirewall brings one VPC's Proxmox firewall state in line with its
// stored rule set. An empty rule set tears the VPC's firewall objects down. It
// holds fwMu for the duration so the periodic loop and a PUT-triggered apply of
// the same VPC can't interleave.
func (s *Server) reconcileVPCFirewall(ctx context.Context, vpcID string, topo sdnTopo) {
	s.fwMu.Lock()
	defer s.fwMu.Unlock()

	rules, err := s.firewallRules(vpcID)
	if err != nil {
		log.Printf("firewall: load rules for vpc %s: %v", vpcID, err)
		return
	}
	group := translate.FirewallGroupName(vpcID)

	if len(rules) == 0 {
		if s.firewallMode() == firewallModeDryRun {
			log.Printf("firewall[dryrun]: vpc %s has no rules -> would tear down group %s", vpcID, group)
			return
		}
		if s.firewallMode() == firewallModeOn {
			s.teardownVPCFirewall(ctx, vpcID, group)
		}
		return
	}

	rc := s.newIPResolveCtx(ctx)
	plan := translate.BuildFirewallPlan(vpcID, rules, s.firewallResolver(ctx, vpcID, rc, topo))
	members := s.vpcMembers(ctx, vpcID, topo)

	if s.firewallMode() == firewallModeDryRun {
		s.logFirewallPlan(vpcID, plan, members)
		return
	}

	s.applyIPSets(ctx, vpcID, plan.IPSets)
	s.applyGroup(ctx, vpcID, group, plan.Rules)
	s.attachFirewallGroup(ctx, group, vpcID, members)
}

// firewallRules loads and deserializes a VPC's stored Oxide rule set.
func (s *Server) firewallRules(vpcID string) ([]oxide.VpcFirewallRule, error) {
	raw, err := s.fwrules.Get(vpcID)
	if err != nil {
		return nil, err
	}
	var rules []oxide.VpcFirewallRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// --- address resolution ---

// firewallResolver returns the live Resolver passed to translate.BuildFirewallPlan:
// it turns a rule side's references (ip/ip_net/subnet/instance/vpc) into a
// Proxmox address spec, synthesizing an IPset when a side resolves to more than
// one address. An unresolvable side degrades to "any" (empty spec).
func (s *Server) firewallResolver(ctx context.Context, vpcID string, rc *ipResolveCtx, topo sdnTopo) translate.Resolver {
	return func(side []translate.FirewallRef) translate.ResolvedRef {
		var members, keyParts []string
		for _, ref := range side {
			keyParts = append(keyParts, ref.Type+":"+ref.Value)
			members = append(members, s.resolveFirewallRef(ctx, ref, rc, topo)...)
		}
		members = dedupStrings(members)
		switch len(members) {
		case 0:
			return translate.ResolvedRef{} // any
		case 1:
			return translate.ResolvedRef{Spec: members[0]}
		default:
			name := translate.FirewallIPSetName(vpcID, strings.Join(keyParts, "_"))
			return translate.ResolvedRef{Spec: "+" + name, IPSet: name, Members: members}
		}
	}
}

// resolveFirewallRef resolves a single host/target reference to zero or more
// Proxmox address specs (literal IPs/CIDRs).
func (s *Server) resolveFirewallRef(ctx context.Context, ref translate.FirewallRef, rc *ipResolveCtx, topo sdnTopo) []string {
	switch ref.Type {
	case "ip", "ip_net":
		return []string{ref.Value}
	case "subnet":
		if cidr := subnetCIDRByRef(ref.Value, topo); cidr != "" {
			return []string{cidr}
		}
	case "instance":
		if r, _ := s.resolveInstance(ctx, ref.Value); r != nil {
			if ip := s.instancePrivateIPWith(ctx, r.vmid, rc); ip != "" {
				return []string{ip}
			}
		}
	case "vpc":
		if id := vpcIDByRef(ref.Value, topo); id != "" {
			return s.vpcMemberIPs(ctx, id, rc, topo)
		}
	}
	return nil
}

// subnetCIDRByRef resolves an Oxide subnet NameOrId to its CIDR via the SDN topo.
func subnetCIDRByRef(ref string, topo sdnTopo) string {
	for _, v := range topo.vnets {
		if ref == v.Vnet || ref == translate.SubnetIDForVnet(v.Vnet) ||
			ref == translate.SanitizeName(v.Vnet, "vnet") {
			return topo.cidrOf(v.Vnet)
		}
	}
	return ""
}

// vpcIDByRef resolves an Oxide VPC NameOrId to its vpc id (SDN zones only).
func vpcIDByRef(ref string, topo sdnTopo) string {
	for _, z := range topo.zones {
		if ref == z.Zone || ref == translate.VPCIDForZone(z.Zone) ||
			ref == translate.SanitizeName(z.Zone, "zone") {
			return translate.VPCIDForZone(z.Zone)
		}
	}
	return ""
}

// --- VPC membership ---

// fwMember is a VM in a VPC plus the NIC devices that attach it to that VPC.
type fwMember struct {
	node string
	vmid int
	nics []string
}

// vpcMembers returns the VMs with at least one NIC on the given (SDN) VPC, and
// the specific netN devices that place them there.
func (s *Server) vpcMembers(ctx context.Context, vpcID string, topo sdnTopo) []fwMember {
	vms, err := s.listVMs(ctx)
	if err != nil {
		return nil
	}
	locate := topo.netLocator()
	var out []fwMember
	for _, vm := range vms {
		cfg, err := s.pve.QemuConfig(ctx, vm.Node, vm.VMID)
		if err != nil {
			continue
		}
		var nics []string
		for i := 0; i < 32; i++ {
			dev := "net" + strconv.Itoa(i)
			val, ok := cfg[dev]
			if !ok {
				continue
			}
			if id, _ := locate(translate.BridgeOf(val)); id == vpcID {
				nics = append(nics, dev)
			}
		}
		if len(nics) > 0 {
			out = append(out, fwMember{node: vm.Node, vmid: vm.VMID, nics: nics})
		}
	}
	return out
}

// vpcMemberIPs returns the private IPs of a VPC's member VMs (for resolving a
// vpc-typed host filter to an IPset).
func (s *Server) vpcMemberIPs(ctx context.Context, vpcID string, rc *ipResolveCtx, topo sdnTopo) []string {
	var ips []string
	for _, m := range s.vpcMembers(ctx, vpcID, topo) {
		if ip := s.instancePrivateIPWith(ctx, m.vmid, rc); ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

// --- apply: IPsets ---

// applyIPSets creates/updates the desired IPsets (members synced) and removes
// this VPC's owned IPsets that the current plan no longer references.
func (s *Server) applyIPSets(ctx context.Context, vpcID string, desired map[string][]string) {
	comment := translate.OwnerComment + vpcID
	existing, err := s.pve.IPSets(ctx)
	if err != nil {
		log.Printf("firewall: list ipsets: %v", err)
		return
	}
	have := map[string]bool{}
	for _, e := range existing {
		have[e.Name] = true
	}
	for name, members := range desired {
		if !have[name] {
			if err := s.pve.CreateIPSet(ctx, name, comment); err != nil {
				log.Printf("firewall: create ipset %s: %v", name, err)
				continue
			}
		}
		s.syncIPSetMembers(ctx, name, members)
	}
	// Drop owned IPsets for this VPC that are no longer in the plan.
	for _, e := range existing {
		if e.Comment == comment && desired[e.Name] == nil {
			s.deleteIPSet(ctx, e.Name)
		}
	}
}

func (s *Server) syncIPSetMembers(ctx context.Context, name string, members []string) {
	current, err := s.pve.IPSetMembers(ctx, name)
	if err != nil {
		log.Printf("firewall: list ipset %s members: %v", name, err)
		return
	}
	want := map[string]bool{}
	for _, m := range members {
		want[m] = true
	}
	have := map[string]bool{}
	for _, m := range current {
		have[m.CIDR] = true
		if !want[m.CIDR] {
			if err := s.pve.DeleteIPSetMember(ctx, name, m.CIDR); err != nil {
				log.Printf("firewall: del ipset member %s/%s: %v", name, m.CIDR, err)
			}
		}
	}
	for _, m := range members {
		if !have[m] {
			if err := s.pve.AddIPSetMember(ctx, name, m, ""); err != nil {
				log.Printf("firewall: add ipset member %s/%s: %v", name, m, err)
			}
		}
	}
}

// deleteIPSet empties then removes an IPset (Proxmox refuses to delete a
// non-empty set).
func (s *Server) deleteIPSet(ctx context.Context, name string) {
	if members, err := s.pve.IPSetMembers(ctx, name); err == nil {
		for _, m := range members {
			_ = s.pve.DeleteIPSetMember(ctx, name, m.CIDR)
		}
	}
	if err := s.pve.DeleteIPSet(ctx, name); err != nil {
		log.Printf("firewall: del ipset %s: %v", name, err)
	}
}

// --- apply: security group ---

// applyGroup ensures the VPC's security group exists and its rules match the
// plan. The group is wholly oxidize-owned, so a mismatch is resolved by clearing
// and recreating the rules in order.
func (s *Server) applyGroup(ctx context.Context, vpcID, group string, rules []translate.PVEGroupRule) {
	groups, err := s.pve.FirewallGroups(ctx)
	if err != nil {
		log.Printf("firewall: list groups: %v", err)
		return
	}
	exists := false
	for _, g := range groups {
		if g.Group == group {
			exists = true
			break
		}
	}
	if !exists {
		if err := s.pve.CreateFirewallGroup(ctx, group, translate.OwnerComment+vpcID); err != nil {
			log.Printf("firewall: create group %s: %v", group, err)
			return
		}
	}

	live, err := s.pve.FirewallGroupRules(ctx, group)
	if err != nil {
		log.Printf("firewall: list group %s rules: %v", group, err)
		return
	}
	// Churn guard: skip the rewrite when this exact rule set was already applied
	// and the live rule count still matches. We compare our own content hash
	// rather than Proxmox's echoed rule fields, so PVE normalizing a field (e.g.
	// dport/proto formatting) can't force a delete-and-recreate every cycle. The
	// count check still heals gross drift (e.g. someone deleted the rules).
	desiredHash := hashFirewallRules(rules)
	if firewallGroupInSync(s.fwApplied[vpcID], desiredHash, len(live), len(rules)) {
		return
	}
	// Delete highest position first so earlier positions stay valid.
	for i := len(live) - 1; i >= 0; i-- {
		if err := s.pve.DeleteFirewallGroupRule(ctx, group, live[i].Pos); err != nil {
			log.Printf("firewall: del group %s rule %d: %v", group, live[i].Pos, err)
		}
	}
	for _, r := range rules {
		if err := s.pve.CreateFirewallGroupRule(ctx, group, groupRuleForm(r)); err != nil {
			log.Printf("firewall: create group %s rule: %v", group, err)
		}
	}
	s.fwApplied[vpcID] = desiredHash
}

// --- apply: attach group to member VMs ---

// attachFirewallGroup ensures each member VM has the firewall enabled (ACCEPT
// baseline), firewall=1 on its VPC NICs, and exactly one group-reference rule;
// and removes the reference from VMs that are no longer members.
func (s *Server) attachFirewallGroup(ctx context.Context, group, vpcID string, members []fwMember) {
	memberSet := map[int]bool{}
	for _, m := range members {
		memberSet[m.vmid] = true
		s.ensureVMFirewallEnabled(ctx, m.node, m.vmid)
		s.ensureNICFirewallFlag(ctx, m.node, m.vmid, m.nics)
		s.ensureGroupRef(ctx, m.node, m.vmid, group, vpcID)
	}
	// Detach VMs that hold our group-ref but are no longer members.
	vms, err := s.listVMs(ctx)
	if err != nil {
		return
	}
	for _, vm := range vms {
		if memberSet[vm.VMID] {
			continue
		}
		s.removeGroupRef(ctx, vm.Node, vm.VMID, group)
	}
}

func (s *Server) ensureVMFirewallEnabled(ctx context.Context, node string, vmid int) {
	opts, err := s.pve.VMFirewallOptions(ctx, node, vmid)
	if err != nil {
		log.Printf("firewall: get vm %d options: %v", vmid, err)
		return
	}
	if opts.Enable == 1 && opts.PolicyIn == "ACCEPT" {
		return
	}
	form := url.Values{}
	form.Set("enable", "1")
	form.Set("policy_in", "ACCEPT")
	if err := s.pve.SetVMFirewallOptions(ctx, node, vmid, form); err != nil {
		log.Printf("firewall: enable vm %d firewall: %v", vmid, err)
	}
}

func (s *Server) ensureNICFirewallFlag(ctx context.Context, node string, vmid int, nics []string) {
	cfg, err := s.pve.QemuConfig(ctx, node, vmid)
	if err != nil {
		return
	}
	for _, dev := range nics {
		val, ok := cfg[dev]
		if !ok {
			continue
		}
		next, changed := nicWithFirewall(val)
		if !changed {
			continue
		}
		form := url.Values{}
		form.Set(dev, next)
		if _, err := s.pve.UpdateConfig(ctx, node, vmid, form); err != nil {
			log.Printf("firewall: set firewall=1 on vm %d %s: %v", vmid, dev, err)
		}
	}
}

func (s *Server) ensureGroupRef(ctx context.Context, node string, vmid int, group, vpcID string) {
	rules, err := s.pve.VMFirewallRules(ctx, node, vmid)
	if err != nil {
		log.Printf("firewall: list vm %d rules: %v", vmid, err)
		return
	}
	for _, r := range rules {
		if r.Type == "group" && r.Action == group {
			return // already attached
		}
	}
	form := url.Values{}
	form.Set("type", "group")
	form.Set("action", group)
	form.Set("enable", "1")
	form.Set("comment", translate.OwnerComment+vpcID)
	if err := s.pve.CreateVMFirewallRule(ctx, node, vmid, form); err != nil {
		log.Printf("firewall: attach group to vm %d: %v", vmid, err)
	}
}

// removeGroupRef deletes any oxidize group-reference rule for group from a VM.
func (s *Server) removeGroupRef(ctx context.Context, node string, vmid int, group string) {
	rules, err := s.pve.VMFirewallRules(ctx, node, vmid)
	if err != nil {
		return
	}
	// Delete highest position first to keep positions valid.
	for i := len(rules) - 1; i >= 0; i-- {
		r := rules[i]
		if r.Type == "group" && r.Action == group {
			if err := s.pve.DeleteVMFirewallRule(ctx, node, vmid, r.Pos); err != nil {
				log.Printf("firewall: detach group from vm %d: %v", vmid, err)
			}
		}
	}
}

// --- teardown + orphan sweep ---

// teardownVPCFirewall removes a VPC's group reference from every VM, then the
// group and its owned IPsets. Used when a VPC's rule set is emptied or its zone
// is deleted.
func (s *Server) teardownVPCFirewall(ctx context.Context, vpcID, group string) {
	if vms, err := s.listVMs(ctx); err == nil {
		for _, vm := range vms {
			s.removeGroupRef(ctx, vm.Node, vm.VMID, group)
		}
	}
	groups, err := s.pve.FirewallGroups(ctx)
	if err == nil {
		for _, g := range groups {
			if g.Group == group {
				// Proxmox refuses to delete a non-empty security group, so clear
				// its rules first (highest position first to keep positions valid).
				if rules, err := s.pve.FirewallGroupRules(ctx, group); err == nil {
					for i := len(rules) - 1; i >= 0; i-- {
						if err := s.pve.DeleteFirewallGroupRule(ctx, group, rules[i].Pos); err != nil {
							log.Printf("firewall: clear group %s rule %d: %v", group, rules[i].Pos, err)
						}
					}
				}
				if err := s.pve.DeleteFirewallGroup(ctx, group); err != nil {
					log.Printf("firewall: delete group %s: %v", group, err)
				}
				break
			}
		}
	}
	comment := translate.OwnerComment + vpcID
	if sets, err := s.pve.IPSets(ctx); err == nil {
		for _, e := range sets {
			if e.Comment == comment {
				s.deleteIPSet(ctx, e.Name)
			}
		}
	}
	delete(s.fwApplied, vpcID)
}

// sweepOrphanFirewall tears down oxidize-owned groups whose VPC is no longer a
// live SDN zone (e.g. the zone was deleted). Live VPCs are handled by
// reconcileVPCFirewall.
func (s *Server) sweepOrphanFirewall(ctx context.Context, live map[string]bool) {
	if s.firewallMode() != firewallModeOn {
		return
	}
	s.fwMu.Lock()
	defer s.fwMu.Unlock()
	groups, err := s.pve.FirewallGroups(ctx)
	if err != nil {
		return
	}
	for _, g := range groups {
		vpcID, ok := ownedVPCID(g.Comment)
		if !ok || live[vpcID] {
			continue
		}
		s.teardownVPCFirewall(ctx, vpcID, g.Group)
	}
}

// --- helpers ---

// logFirewallPlan reports, in dry-run mode, exactly what would be written.
func (s *Server) logFirewallPlan(vpcID string, plan translate.FirewallPlan, members []fwMember) {
	log.Printf("firewall[dryrun]: vpc %s -> group %s (%d rules, %d ipsets, %d member VMs)",
		vpcID, plan.GroupName, len(plan.Rules), len(plan.IPSets), len(members))
	for _, r := range plan.Rules {
		log.Printf("firewall[dryrun]:   rule %s %s proto=%q dport=%q src=%q dst=%q enable=%d (%s)",
			r.Type, r.Action, r.Proto, r.Dport, r.Source, r.Dest, r.Enable, r.Comment)
	}
	for name, m := range plan.IPSets {
		log.Printf("firewall[dryrun]:   ipset %s = %v", name, m)
	}
	for _, m := range members {
		log.Printf("firewall[dryrun]:   attach group %s to vm %d (nics %v)", plan.GroupName, m.vmid, m.nics)
	}
}

// groupRuleForm builds the PVE form for creating a security-group rule.
func groupRuleForm(r translate.PVEGroupRule) url.Values {
	f := url.Values{}
	f.Set("type", r.Type)
	f.Set("action", r.Action)
	f.Set("enable", strconv.Itoa(r.Enable))
	if r.Proto != "" {
		f.Set("proto", r.Proto)
	}
	if r.Dport != "" {
		f.Set("dport", r.Dport)
	}
	if r.Source != "" {
		f.Set("source", r.Source)
	}
	if r.Dest != "" {
		f.Set("dest", r.Dest)
	}
	if r.Comment != "" {
		f.Set("comment", r.Comment)
	}
	return f
}

// firewallGroupInSync reports whether a VPC's security group can be left
// untouched: the exact rule set was already applied (same content hash) and the
// live rule count still matches (so it hasn't drifted, e.g. been deleted).
func firewallGroupInSync(appliedHash, desiredHash string, liveCount, desiredCount int) bool {
	return appliedHash != "" && appliedHash == desiredHash && liveCount == desiredCount
}

// hashFirewallRules is a stable, order-sensitive content hash of a desired rule
// set, used as the churn-guard key. It hashes our own canonical signature of
// each rule, independent of how Proxmox echoes the fields back.
func hashFirewallRules(rules []translate.PVEGroupRule) string {
	h := sha1.New()
	for _, r := range rules {
		io.WriteString(h, desiredRuleSig(r))
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func desiredRuleSig(r translate.PVEGroupRule) string {
	return strings.Join([]string{r.Type, r.Action, r.Proto, r.Dport, r.Source, r.Dest, strconv.Itoa(r.Enable)}, "|")
}

// nicWithFirewall returns the NIC config value with firewall=1 set, and whether
// it changed.
func nicWithFirewall(val string) (string, bool) {
	if strings.Contains(val, "firewall=1") {
		return val, false
	}
	if strings.Contains(val, "firewall=0") {
		return strings.Replace(val, "firewall=0", "firewall=1", 1), true
	}
	return val + ",firewall=1", true
}

// ownedVPCID extracts the VPC id from an oxidize ownership comment
// ("oxidize:<vpcID>" optionally followed by more text).
func ownedVPCID(comment string) (string, bool) {
	if !strings.HasPrefix(comment, translate.OwnerComment) {
		return "", false
	}
	rest := strings.TrimPrefix(comment, translate.OwnerComment)
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

func dedupStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := map[string]bool{}
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

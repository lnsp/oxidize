package store

import (
	"encoding/json"
	"path/filepath"
	"sync"
)

// FirewallRuleStore persists VPC firewall rule sets as a JSON file, keyed by
// VPC id.
//
// IMPORTANT: these rules are RECORDED but NOT ENFORCED. Oxide's VPC-scoped
// firewall model (targets/filters/host-filters, priority, allow|deny,
// inbound|outbound) does not map cleanly or safely onto Proxmox's per-VM/cluster
// firewall, and oxidize already treats VPCs as synthetic/SDN-derived. So a VPC's
// rule set is stored here and round-trips to the console (the firewall-rules page
// is fully functional: edit, save, reload), but the rules are NOT applied to the
// Proxmox data plane. This is consistent with the rest of the synthetic VPC
// surface (subnets, routers).
//
// Each entry holds the rule set as its serialized Oxide read shape
// ([]oxide.VpcFirewallRule) in Rules, kept as raw JSON so the store stays
// decoupled from the wire structs and the polymorphic target/filter unions
// round-trip untouched.
type firewallRuleEntry struct {
	VpcID string          `json:"vpc_id"`
	Rules json.RawMessage `json:"rules"`
}

// FirewallRuleStore is a mutex-guarded, file-backed firewall rule store.
type FirewallRuleStore struct {
	path string
	mu   sync.Mutex
}

// NewFirewallRuleStore returns a store backed by <dir>/firewall-rules.json.
func NewFirewallRuleStore(dir string) *FirewallRuleStore {
	return &FirewallRuleStore{path: filepath.Join(dir, "firewall-rules.json")}
}

func (s *FirewallRuleStore) load() ([]firewallRuleEntry, error) {
	var entries []firewallRuleEntry
	err := readJSON(s.path, &entries)
	return entries, err
}

func (s *FirewallRuleStore) save(entries []firewallRuleEntry) error {
	return writeJSON(s.path, entries)
}

// Get returns the serialized rule set ([]oxide.VpcFirewallRule as raw JSON) for
// vpcID. A VPC with no stored rules returns a JSON empty array, not an error.
func (s *FirewallRuleStore) Get(vpcID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.VpcID == vpcID {
			return e.Rules, nil
		}
	}
	return json.RawMessage("[]"), nil
}

// Replace overwrites the entire rule set for vpcID with rules (the serialized
// []oxide.VpcFirewallRule), per the Oxide update semantics (the update replaces
// all existing rules).
func (s *FirewallRuleStore) Replace(vpcID string, rules json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return err
	}
	for i := range entries {
		if entries[i].VpcID == vpcID {
			entries[i].Rules = rules
			return s.save(entries)
		}
	}
	entries = append(entries, firewallRuleEntry{VpcID: vpcID, Rules: rules})
	return s.save(entries)
}

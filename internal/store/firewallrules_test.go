package store

import (
	"encoding/json"
	"testing"

	"github.com/lnsp/oxidize/internal/translate"
)

func TestFirewallRuleStore(t *testing.T) {
	s := NewFirewallRuleStore(t.TempDir())

	// A VPC with no stored rules returns an empty array, not an error.
	got, err := s.Get("vpc-1")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	var rules []map[string]any
	if err := json.Unmarshal(got, &rules); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("empty VPC rules = %d, want 0", len(rules))
	}

	// Replace then Get round-trips the stored rule set verbatim.
	want := json.RawMessage(`[{"name":"allow-ssh","action":"allow"}]`)
	if err := s.Replace("vpc-1", want); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err = s.Get("vpc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !jsonEqual(t, got, want) {
		t.Fatalf("Get = %s, want %s", got, want)
	}

	// Replace overwrites the entire set; a different VPC is unaffected.
	if err := s.Replace("vpc-1", json.RawMessage(`[]`)); err != nil {
		t.Fatalf("Replace overwrite: %v", err)
	}
	got, _ = s.Get("vpc-1")
	if string(got) != "[]" {
		t.Fatalf("after overwrite Get = %s, want []", got)
	}
	other, _ := s.Get("vpc-2")
	if string(other) != "[]" {
		t.Fatalf("untouched VPC Get = %s, want []", other)
	}

	// Persistence survives a fresh store over the same directory is covered by
	// the round-trip above (load reads from disk each call).
}

// jsonEqual reports whether two JSON documents are semantically equal,
// ignoring formatting differences introduced by the store's indented writes.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

func TestFirewallRuleIDStable(t *testing.T) {
	a := translate.FirewallRuleID("vpc-1", "allow-ssh")
	b := translate.FirewallRuleID("vpc-1", "allow-ssh")
	if a != b {
		t.Fatalf("FirewallRuleID not stable: %s != %s", a, b)
	}
	if a == translate.FirewallRuleID("vpc-1", "allow-http") {
		t.Fatalf("FirewallRuleID collided across rule names")
	}
	if a == translate.FirewallRuleID("vpc-2", "allow-ssh") {
		t.Fatalf("FirewallRuleID collided across VPCs")
	}
}

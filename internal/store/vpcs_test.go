package store

import (
	"testing"
	"time"
)

func sp(s string) *string { return &s }

func TestVPCStore(t *testing.T) {
	s := NewVPCStore(t.TempDir())
	now := time.Now()

	mk := func(id, zone, proj, name string) VPC {
		return VPC{ID: id, Zone: zone, ProjectID: proj, Name: name, DNSName: name, TimeCreated: now, TimeModified: now}
	}

	if _, err := s.Create(mk("id-a", "za", "p1", "web")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Duplicate name within the same project is rejected.
	if _, err := s.Create(mk("id-b", "zb", "p1", "web")); err != ErrVPCNameTaken {
		t.Fatalf("dup name same project: want ErrVPCNameTaken, got %v", err)
	}
	// Same name in a different project is allowed (different zone).
	if _, err := s.Create(mk("id-c", "zc", "p2", "web")); err != nil {
		t.Fatalf("dup name cross project should be allowed: %v", err)
	}

	// Get by id, zone, and name.
	for _, ref := range []string{"id-a", "za"} {
		if _, ok, _ := s.Get(ref); !ok {
			t.Errorf("Get(%q) not found", ref)
		}
	}
	// ForProject filters by owner.
	if p1, _ := s.ForProject("p1"); len(p1) != 1 {
		t.Errorf("ForProject(p1) = %d, want 1", len(p1))
	}

	// Rename: name changes, zone stays.
	upd, ok, err := s.Update("id-a", sp("frontend"), sp("the front"), nil, now)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if upd.Name != "frontend" || upd.Zone != "za" || upd.Description != "the front" {
		t.Fatalf("rename result: %+v", upd)
	}

	// Rename collision within a project is rejected.
	if _, err := s.Create(mk("id-d", "zd", "p1", "api")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Update("id-d", sp("frontend"), nil, nil, now); err != ErrVPCNameTaken {
		t.Errorf("rename collision: want ErrVPCNameTaken, got %v", err)
	}

	// Delete.
	if ok, _ := s.Delete("id-a"); !ok {
		t.Error("delete returned false")
	}
	if _, ok, _ := s.Get("frontend"); ok {
		t.Error("record still present after delete")
	}
}

package store

import (
	"testing"
	"time"
)

func TestAffinityGroupStore(t *testing.T) {
	now := time.Now().UTC()
	s := NewAffinityGroupStore(t.TempDir())

	g := AffinityGroup{
		ID:            "id-1",
		Kind:          KindAffinity,
		Name:          "web",
		Description:   "web tier",
		ProjectID:     "proj-1",
		Policy:        "allow",
		FailureDomain: "sled",
		TimeCreated:   now,
		TimeModified:  now,
	}
	if err := s.Create(g); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Duplicate name within the same kind+project is rejected.
	if err := s.Create(g); err != ErrGroupNameTaken {
		t.Fatalf("duplicate Create err = %v, want ErrGroupNameTaken", err)
	}

	// Same name but a different kind is allowed (separate namespaces).
	anti := g
	anti.ID = "id-2"
	anti.Kind = KindAntiAffinity
	if err := s.Create(anti); err != nil {
		t.Fatalf("Create anti-affinity with same name: %v", err)
	}

	// Get by id and by name, scoped to kind.
	got, ok, err := s.Get(KindAffinity, "web")
	if err != nil || !ok {
		t.Fatalf("Get by name: ok=%v err=%v", ok, err)
	}
	if got.ID != "id-1" {
		t.Fatalf("Get returned id %q, want id-1", got.ID)
	}
	if _, ok, _ := s.Get(KindAffinity, "id-2"); ok {
		t.Fatal("Get found the anti-affinity group under the affinity kind")
	}

	// List filters by kind and project.
	list, err := s.List(KindAffinity, "proj-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d groups, want 1", len(list))
	}
	if got, _ := s.List(KindAffinity, "other-proj"); len(got) != 0 {
		t.Fatalf("List for other project returned %d, want 0", len(got))
	}

	// Update changes the description and policy.
	desc := "frontend tier"
	policy := "fail"
	upd, ok, err := s.Update(KindAffinity, "web", &desc, &policy, now.Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("Update: ok=%v err=%v", ok, err)
	}
	if upd.Description != desc || upd.Policy != policy {
		t.Fatalf("Update = (%q,%q), want (%q,%q)", upd.Description, upd.Policy, desc, policy)
	}
	if !upd.TimeModified.After(now) {
		t.Fatal("Update did not bump TimeModified")
	}

	// AddMember and reverse lookup.
	if _, ok, err := s.AddMember(KindAffinity, "web", 100, now); err != nil || !ok {
		t.Fatalf("AddMember: ok=%v err=%v", ok, err)
	}
	if _, _, err := s.AddMember(KindAffinity, "web", 100, now); err != ErrMemberExists {
		t.Fatalf("re-AddMember err = %v, want ErrMemberExists", err)
	}
	if _, ok, _ := s.AddMember(KindAffinity, "web", 101, now); !ok {
		t.Fatal("AddMember second member failed")
	}
	got, _, _ = s.Get(KindAffinity, "web")
	if len(got.Members) != 2 {
		t.Fatalf("group has %d members, want 2", len(got.Members))
	}

	rev, err := s.GroupsForInstance(KindAffinity, "", 100)
	if err != nil {
		t.Fatalf("GroupsForInstance: %v", err)
	}
	if len(rev) != 1 || rev[0].ID != "id-1" {
		t.Fatalf("reverse lookup = %+v, want the web group", rev)
	}
	if got, _ := s.GroupsForInstance(KindAntiAffinity, "", 100); len(got) != 0 {
		t.Fatalf("reverse lookup leaked across kinds: %+v", got)
	}

	// RemoveMember.
	if ok, err := s.RemoveMember(KindAffinity, "web", 100, now); err != nil || !ok {
		t.Fatalf("RemoveMember: ok=%v err=%v", ok, err)
	}
	if _, err := s.RemoveMember(KindAffinity, "web", 100, now); err != ErrMemberNotFound {
		t.Fatalf("removing an absent member err = %v, want ErrMemberNotFound", err)
	}
	if got, _ := s.GroupsForInstance(KindAffinity, "", 100); len(got) != 0 {
		t.Fatal("instance still appears in reverse lookup after removal")
	}

	// Delete is kind-scoped.
	if ok, _ := s.Delete(KindAffinity, "web"); !ok {
		t.Fatal("Delete affinity group failed")
	}
	if _, ok, _ := s.Get(KindAffinity, "web"); ok {
		t.Fatal("group still present after Delete")
	}
	if _, ok, _ := s.Get(KindAntiAffinity, "web"); !ok {
		t.Fatal("Delete removed the anti-affinity group of the same name")
	}
}

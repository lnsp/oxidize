package store

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// AffinityGroup is a persisted (anti-)affinity group.
//
// IMPORTANT: these groups are RECORDED but NOT ENFORCED. Oxide affinity groups
// influence instance placement on sleds, but oxidize does not control VM
// placement — Proxmox does, and it has no version-stable equivalent concept. So
// the groups and their membership are stored here and surfaced to the console
// (making the affinity pages fully functional CRUD), but membership has no
// effect on where Proxmox actually schedules a VM. The same record type serves
// both affinity and anti-affinity groups, distinguished by Kind.
type AffinityGroup struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"` // "affinity" | "anti-affinity"
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	ProjectID     string    `json:"project_id"`
	Policy        string    `json:"policy"`         // "allow" | "fail"
	FailureDomain string    `json:"failure_domain"` // "sled"
	Members       []int     `json:"members"`        // instance vmids
	TimeCreated   time.Time `json:"time_created"`
	TimeModified  time.Time `json:"time_modified"`
}

// Group kinds.
const (
	KindAffinity     = "affinity"
	KindAntiAffinity = "anti-affinity"
)

// AffinityGroupStore persists (anti-)affinity groups as a JSON file.
type AffinityGroupStore struct {
	path string
	mu   sync.Mutex
}

// NewAffinityGroupStore returns a store backed by <dir>/affinity-groups.json.
func NewAffinityGroupStore(dir string) *AffinityGroupStore {
	return &AffinityGroupStore{path: filepath.Join(dir, "affinity-groups.json")}
}

func (s *AffinityGroupStore) load() ([]AffinityGroup, error) {
	var groups []AffinityGroup
	err := readJSON(s.path, &groups)
	return groups, err
}

func (s *AffinityGroupStore) save(groups []AffinityGroup) error {
	return writeJSON(s.path, groups)
}

// ErrGroupNameTaken is returned by Create when a group of the same kind already
// exists with that name in the project; ErrMemberNotFound by RemoveMember when
// the instance wasn't a member. Membership-conflict reuses the package-level
// ErrMemberExists (shared with the subnet-pool store).
var (
	ErrGroupNameTaken = errors.New("a group with that name already exists in this project")
	ErrMemberNotFound = errors.New("instance is not a member of this group")
)

// List returns all groups of kind in projectID. A blank projectID or kind acts
// as a wildcard for that field.
func (s *AffinityGroupStore) List(kind, projectID string) ([]AffinityGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return nil, err
	}
	out := []AffinityGroup{}
	for _, g := range groups {
		if kind != "" && g.Kind != kind {
			continue
		}
		if projectID != "" && g.ProjectID != projectID {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

// Get resolves a group of kind by id or name. A blank kind matches any kind.
func (s *AffinityGroupStore) Get(kind, ref string) (AffinityGroup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return AffinityGroup{}, false, err
	}
	for _, g := range groups {
		if kind != "" && g.Kind != kind {
			continue
		}
		if g.ID == ref || g.Name == ref {
			return g, true, nil
		}
	}
	return AffinityGroup{}, false, nil
}

// Create stores a new group, rejecting a duplicate name within the same
// project+kind.
func (s *AffinityGroupStore) Create(g AffinityGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range groups {
		if existing.Kind == g.Kind && existing.ProjectID == g.ProjectID && existing.Name == g.Name {
			return ErrGroupNameTaken
		}
	}
	groups = append(groups, g)
	return s.save(groups)
}

// Update mutates a group's description and/or policy in place (the only fields
// the Oxide update accepts; pass nil to leave one unchanged) and returns the
// updated record.
func (s *AffinityGroupStore) Update(kind, ref string, description, policy *string, now time.Time) (AffinityGroup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return AffinityGroup{}, false, err
	}
	for i := range groups {
		if kind != "" && groups[i].Kind != kind {
			continue
		}
		if groups[i].ID == ref || groups[i].Name == ref {
			if description != nil {
				groups[i].Description = *description
			}
			if policy != nil {
				groups[i].Policy = *policy
			}
			groups[i].TimeModified = now
			return groups[i], true, s.save(groups)
		}
	}
	return AffinityGroup{}, false, nil
}

// Delete removes a group of kind by id or name. Returns true if one was removed.
func (s *AffinityGroupStore) Delete(kind, ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return false, err
	}
	out := groups[:0]
	removed := false
	for _, g := range groups {
		if (kind == "" || g.Kind == kind) && (g.ID == ref || g.Name == ref) {
			removed = true
			continue
		}
		out = append(out, g)
	}
	if !removed {
		return false, nil
	}
	return true, s.save(out)
}

// AddMember adds an instance vmid to a group, returning the updated record.
// ok=false means the group doesn't exist; ErrMemberExists if already a member.
func (s *AffinityGroupStore) AddMember(kind, ref string, vmid int, now time.Time) (AffinityGroup, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return AffinityGroup{}, false, err
	}
	for i := range groups {
		if kind != "" && groups[i].Kind != kind {
			continue
		}
		if groups[i].ID == ref || groups[i].Name == ref {
			for _, m := range groups[i].Members {
				if m == vmid {
					return AffinityGroup{}, true, ErrMemberExists
				}
			}
			groups[i].Members = append(groups[i].Members, vmid)
			groups[i].TimeModified = now
			return groups[i], true, s.save(groups)
		}
	}
	return AffinityGroup{}, false, nil
}

// RemoveMember drops an instance vmid from a group. ok=false means the group
// doesn't exist; ErrMemberNotFound if the instance wasn't a member.
func (s *AffinityGroupStore) RemoveMember(kind, ref string, vmid int, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.load()
	if err != nil {
		return false, err
	}
	for i := range groups {
		if kind != "" && groups[i].Kind != kind {
			continue
		}
		if groups[i].ID == ref || groups[i].Name == ref {
			members := groups[i].Members[:0]
			found := false
			for _, m := range groups[i].Members {
				if m == vmid {
					found = true
					continue
				}
				members = append(members, m)
			}
			if !found {
				return true, ErrMemberNotFound
			}
			groups[i].Members = members
			groups[i].TimeModified = now
			return true, s.save(groups)
		}
	}
	return false, nil
}

// GroupsForInstance returns the groups of kind in projectID that contain vmid
// (the per-instance reverse lookup). A blank projectID matches any project.
func (s *AffinityGroupStore) GroupsForInstance(kind, projectID string, vmid int) ([]AffinityGroup, error) {
	groups, err := s.List(kind, projectID)
	if err != nil {
		return nil, err
	}
	out := []AffinityGroup{}
	for _, g := range groups {
		for _, m := range g.Members {
			if m == vmid {
				out = append(out, g)
				break
			}
		}
	}
	return out, nil
}

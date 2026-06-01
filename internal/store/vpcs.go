package store

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// VPC records the link between a Proxmox SDN zone and the project that owns it,
// plus the human-readable display fields. oxidize maps a VPC to an SDN zone, but
// zone names are cluster-global and capped at 8 chars, so the project ownership
// and the real (renamable) name/description/dns live here rather than in the
// zone name. Zones with no record (created outside oxidize, e.g. the pre-existing
// "lab" zone) are treated as global and shown in every project.
//
// Zone is the immutable key (Proxmox can't rename a zone). ID is the derived
// Oxide VPC id (VPCIDForZone(Zone)), persisted so the store needs no translate
// import. Name/Description/DNSName are mutable display fields.
type VPC struct {
	ID           string    `json:"id"`
	Zone         string    `json:"zone"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	DNSName      string    `json:"dns_name"`
	TimeCreated  time.Time `json:"time_created"`
	TimeModified time.Time `json:"time_modified"`
}

// VPCStore persists project-scoped VPC records as a JSON file.
type VPCStore struct {
	path string
	mu   sync.Mutex
}

// NewVPCStore returns a store backed by <dir>/vpcs.json.
func NewVPCStore(dir string) *VPCStore {
	return &VPCStore{path: filepath.Join(dir, "vpcs.json")}
}

// ErrVPCNameTaken is returned by Create when the project already has a VPC with
// that name.
var ErrVPCNameTaken = errors.New("a VPC with that name already exists in this project")

func (s *VPCStore) load() ([]VPC, error) {
	var vpcs []VPC
	err := readJSON(s.path, &vpcs)
	return vpcs, err
}

func (s *VPCStore) save(vpcs []VPC) error {
	return writeJSON(s.path, vpcs)
}

// List returns all VPC records.
func (s *VPCStore) List() ([]VPC, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Get resolves a VPC record by id, zone, or name.
func (s *VPCStore) Get(ref string) (VPC, bool, error) {
	vpcs, err := s.List()
	if err != nil {
		return VPC{}, false, err
	}
	for _, v := range vpcs {
		if v.ID == ref || v.Zone == ref || v.Name == ref {
			return v, true, nil
		}
	}
	return VPC{}, false, nil
}

// ByZone resolves the record for an exact zone name.
func (s *VPCStore) ByZone(zone string) (VPC, bool, error) {
	vpcs, err := s.List()
	if err != nil {
		return VPC{}, false, err
	}
	for _, v := range vpcs {
		if v.Zone == zone {
			return v, true, nil
		}
	}
	return VPC{}, false, nil
}

// ForProject returns the VPC records owned by a project.
func (s *VPCStore) ForProject(projectID string) ([]VPC, error) {
	vpcs, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]VPC, 0, len(vpcs))
	for _, v := range vpcs {
		if v.ProjectID == projectID {
			out = append(out, v)
		}
	}
	return out, nil
}

// Create stores a new VPC record, rejecting a duplicate name within the same
// project (names may repeat across projects, since their zones differ).
func (s *VPCStore) Create(v VPC) (VPC, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vpcs, err := s.load()
	if err != nil {
		return VPC{}, err
	}
	for _, e := range vpcs {
		if e.ProjectID == v.ProjectID && e.Name == v.Name {
			return VPC{}, ErrVPCNameTaken
		}
	}
	vpcs = append(vpcs, v)
	return v, s.save(vpcs)
}

// Update mutates a record's display fields (name/description/dns). The zone is
// immutable. A name change is rejected if it collides with another VPC in the
// same project.
func (s *VPCStore) Update(ref string, name, desc, dns *string, now time.Time) (VPC, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vpcs, err := s.load()
	if err != nil {
		return VPC{}, false, err
	}
	idx := -1
	for i := range vpcs {
		if vpcs[i].ID == ref || vpcs[i].Zone == ref || vpcs[i].Name == ref {
			idx = i
			break
		}
	}
	if idx == -1 {
		return VPC{}, false, nil
	}
	if name != nil && *name != "" && *name != vpcs[idx].Name {
		for i := range vpcs {
			if i != idx && vpcs[i].ProjectID == vpcs[idx].ProjectID && vpcs[i].Name == *name {
				return VPC{}, false, ErrVPCNameTaken
			}
		}
		vpcs[idx].Name = *name
	}
	if desc != nil {
		vpcs[idx].Description = *desc
	}
	if dns != nil && *dns != "" {
		vpcs[idx].DNSName = *dns
	}
	vpcs[idx].TimeModified = now
	return vpcs[idx], true, s.save(vpcs)
}

// Delete removes a VPC record by id/zone/name. Returns true if one was removed.
func (s *VPCStore) Delete(ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vpcs, err := s.load()
	if err != nil {
		return false, err
	}
	out := vpcs[:0]
	removed := false
	for _, v := range vpcs {
		if v.ID == ref || v.Zone == ref || v.Name == ref {
			removed = true
			continue
		}
		out = append(out, v)
	}
	if !removed {
		return false, nil
	}
	return true, s.save(out)
}

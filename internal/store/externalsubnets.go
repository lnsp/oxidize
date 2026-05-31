package store

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// ExternalSubnet is a routable CIDR allocated from a subnet pool and (optionally)
// attached to an instance. Non-opaque: takahe routes the block to the instance,
// no NAT. InstanceVMID is 0 when detached.
type ExternalSubnet struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	ProjectID          string    `json:"project_id"`
	Subnet             string    `json:"subnet"` // CIDR
	SubnetPoolID       string    `json:"subnet_pool_id"`
	SubnetPoolMemberID string    `json:"subnet_pool_member_id"`
	InstanceVMID       int       `json:"instance_vmid"`
	TimeCreated        time.Time `json:"time_created"`
	TimeModified       time.Time `json:"time_modified"`
}

// ExternalSubnetStore persists external subnets as a JSON file.
type ExternalSubnetStore struct {
	path string
	mu   sync.Mutex
}

func NewExternalSubnetStore(dir string) *ExternalSubnetStore {
	return &ExternalSubnetStore{path: filepath.Join(dir, "external-subnets.json")}
}

var (
	ErrExtSubnetNameTaken = errors.New("an external subnet with that name already exists")
	ErrExtSubnetTaken     = errors.New("that subnet is already allocated")
)

func (s *ExternalSubnetStore) load() ([]ExternalSubnet, error) {
	var subs []ExternalSubnet
	err := readJSON(s.path, &subs)
	return subs, err
}

func (s *ExternalSubnetStore) save(subs []ExternalSubnet) error {
	return writeJSON(s.path, subs)
}

func (s *ExternalSubnetStore) List() ([]ExternalSubnet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *ExternalSubnetStore) Get(ref string) (ExternalSubnet, bool, error) {
	subs, err := s.List()
	if err != nil {
		return ExternalSubnet{}, false, err
	}
	for _, e := range subs {
		if e.ID == ref || e.Name == ref || e.Subnet == ref {
			return e, true, nil
		}
	}
	return ExternalSubnet{}, false, nil
}

func (s *ExternalSubnetStore) Create(e ExternalSubnet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := s.load()
	if err != nil {
		return err
	}
	for _, x := range subs {
		if x.Name == e.Name {
			return ErrExtSubnetNameTaken
		}
		if x.Subnet == e.Subnet {
			return ErrExtSubnetTaken
		}
	}
	subs = append(subs, e)
	return s.save(subs)
}

// CreateAllocated atomically chooses and persists an external subnet. pick is
// called under the store lock with the CIDRs already in use and returns the
// (cidr, memberID) to assign — it must avoid overlapping any used CIDR — or
// ("", "") if nothing fits. Selecting and persisting in one critical section
// prevents concurrent allocations from colliding. rec.Subnet/SubnetPoolMemberID
// are ignored (pick supplies them).
func (s *ExternalSubnetStore) CreateAllocated(rec ExternalSubnet, pick func(used []string) (cidr, memberID string)) (ExternalSubnet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := s.load()
	if err != nil {
		return ExternalSubnet{}, err
	}
	used := make([]string, 0, len(subs))
	for _, e := range subs {
		if e.Name == rec.Name {
			return ExternalSubnet{}, ErrExtSubnetNameTaken
		}
		used = append(used, e.Subnet)
	}
	cidr, memberID := pick(used)
	if cidr == "" {
		return ExternalSubnet{}, ErrExtSubnetTaken
	}
	rec.Subnet = cidr
	rec.SubnetPoolMemberID = memberID
	subs = append(subs, rec)
	return rec, s.save(subs)
}

func (s *ExternalSubnetStore) Delete(ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := s.load()
	if err != nil {
		return false, err
	}
	out := subs[:0]
	removed := false
	for _, e := range subs {
		if e.ID == ref || e.Name == ref || e.Subnet == ref {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return false, nil
	}
	return true, s.save(out)
}

func (s *ExternalSubnetStore) SetAttachment(ref string, vmid int, now time.Time) (ExternalSubnet, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := s.load()
	if err != nil {
		return ExternalSubnet{}, false, err
	}
	for i := range subs {
		if subs[i].ID == ref || subs[i].Name == ref || subs[i].Subnet == ref {
			subs[i].InstanceVMID = vmid
			subs[i].TimeModified = now
			return subs[i], true, s.save(subs)
		}
	}
	return ExternalSubnet{}, false, nil
}

// UsedCIDRs returns the allocated subnet CIDRs (for allocation overlap checks).
func (s *ExternalSubnetStore) UsedCIDRs() ([]string, error) {
	subs, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(subs))
	for _, e := range subs {
		out = append(out, e.Subnet)
	}
	return out, nil
}

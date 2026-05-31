package store

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// SubnetPoolMember is one CIDR block belonging to a subnet pool, with the
// prefix-length bounds for allocations drawn from it.
type SubnetPoolMember struct {
	ID              string    `json:"id"`
	Subnet          string    `json:"subnet"` // CIDR
	MinPrefixLength int       `json:"min_prefix_length"`
	MaxPrefixLength int       `json:"max_prefix_length"`
	TimeCreated     time.Time `json:"time_created"`
}

// SubnetPool is an admin-managed pool of subnet CIDRs (System -> Networking ->
// Subnet Pools). One synthetic silo, so the silo link collapses to IsDefault.
type SubnetPool struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	IPVersion    string             `json:"ip_version"`
	IsDefault    bool               `json:"is_default"`
	Members      []SubnetPoolMember `json:"members"`
	TimeCreated  time.Time          `json:"time_created"`
	TimeModified time.Time          `json:"time_modified"`
}

// SubnetPoolStore persists subnet pools as a JSON file.
type SubnetPoolStore struct {
	path string
	mu   sync.Mutex
}

// NewSubnetPoolStore returns a store backed by <dir>/subnet-pools.json.
func NewSubnetPoolStore(dir string) *SubnetPoolStore {
	return &SubnetPoolStore{path: filepath.Join(dir, "subnet-pools.json")}
}

var (
	ErrSubnetPoolNameTaken = errors.New("a subnet pool with that name already exists")
	ErrMemberExists        = errors.New("that subnet is already a member of the pool")
)

func (s *SubnetPoolStore) load() ([]SubnetPool, error) {
	var pools []SubnetPool
	err := readJSON(s.path, &pools)
	return pools, err
}

func (s *SubnetPoolStore) save(pools []SubnetPool) error {
	return writeJSON(s.path, pools)
}

func (s *SubnetPoolStore) List() ([]SubnetPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *SubnetPoolStore) Get(ref string) (SubnetPool, bool, error) {
	pools, err := s.List()
	if err != nil {
		return SubnetPool{}, false, err
	}
	for _, p := range pools {
		if p.ID == ref || p.Name == ref {
			return p, true, nil
		}
	}
	return SubnetPool{}, false, nil
}

func (s *SubnetPoolStore) Create(p SubnetPool) (SubnetPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return SubnetPool{}, err
	}
	hasDefault := false
	for _, e := range pools {
		if e.Name == p.Name {
			return SubnetPool{}, ErrSubnetPoolNameTaken
		}
		if e.IsDefault && e.IPVersion == p.IPVersion {
			hasDefault = true
		}
	}
	if !hasDefault {
		p.IsDefault = true
	}
	if p.Members == nil {
		p.Members = []SubnetPoolMember{}
	}
	pools = append(pools, p)
	return p, s.save(pools)
}

func (s *SubnetPoolStore) Update(ref string, name, desc *string, now time.Time) (SubnetPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return SubnetPool{}, false, err
	}
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			if name != nil && *name != "" {
				pools[i].Name = *name
			}
			if desc != nil {
				pools[i].Description = *desc
			}
			pools[i].TimeModified = now
			return pools[i], true, s.save(pools)
		}
	}
	return SubnetPool{}, false, nil
}

func (s *SubnetPoolStore) Delete(ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return false, err
	}
	out := pools[:0]
	removed := false
	for _, p := range pools {
		if p.ID == ref || p.Name == ref {
			removed = true
			continue
		}
		out = append(out, p)
	}
	if !removed {
		return false, nil
	}
	return true, s.save(out)
}

func (s *SubnetPoolStore) AddMember(ref string, m SubnetPoolMember, now time.Time) (SubnetPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return SubnetPool{}, false, err
	}
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			for _, e := range pools[i].Members {
				if e.Subnet == m.Subnet {
					return SubnetPool{}, false, ErrMemberExists
				}
			}
			pools[i].Members = append(pools[i].Members, m)
			pools[i].TimeModified = now
			return pools[i], true, s.save(pools)
		}
	}
	return SubnetPool{}, false, nil
}

func (s *SubnetPoolStore) RemoveMember(ref, subnet string, now time.Time) (SubnetPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return SubnetPool{}, false, err
	}
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			out := pools[i].Members[:0]
			for _, e := range pools[i].Members {
				if e.Subnet == subnet {
					continue
				}
				out = append(out, e)
			}
			pools[i].Members = out
			pools[i].TimeModified = now
			return pools[i], true, s.save(pools)
		}
	}
	return SubnetPool{}, false, nil
}

func (s *SubnetPoolStore) SetDefault(ref string, isDefault bool, now time.Time) (SubnetPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return SubnetPool{}, false, err
	}
	idx := -1
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			idx = i
			break
		}
	}
	if idx == -1 {
		return SubnetPool{}, false, nil
	}
	if isDefault {
		for i := range pools {
			if pools[i].IPVersion == pools[idx].IPVersion {
				pools[i].IsDefault = false
			}
		}
	}
	pools[idx].IsDefault = isDefault
	pools[idx].TimeModified = now
	return pools[idx], true, s.save(pools)
}

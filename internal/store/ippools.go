package store

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// IPRange is an inclusive address range belonging to an IP pool.
type IPRange struct {
	ID          string    `json:"id"`
	First       string    `json:"first"`
	Last        string    `json:"last"`
	TimeCreated time.Time `json:"time_created"`
}

// IPPool is an admin-managed pool of external addresses. There is a single
// synthetic silo, so a pool's silo link collapses to the IsDefault flag.
type IPPool struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	IPVersion    string    `json:"ip_version"` // "v4" | "v6"
	PoolType     string    `json:"pool_type"`  // "unicast" | "multicast"
	IsDefault    bool      `json:"is_default"`
	Ranges       []IPRange `json:"ranges"`
	TimeCreated  time.Time `json:"time_created"`
	TimeModified time.Time `json:"time_modified"`
}

// IPPoolStore persists admin-managed IP pools as a JSON file.
type IPPoolStore struct {
	path string
	mu   sync.Mutex
}

// NewIPPoolStore returns a store backed by <dir>/ip-pools.json.
func NewIPPoolStore(dir string) *IPPoolStore {
	return &IPPoolStore{path: filepath.Join(dir, "ip-pools.json")}
}

var (
	ErrPoolNameTaken = errors.New("an IP pool with that name already exists")
	ErrPoolNotFound  = errors.New("IP pool not found")
	ErrRangeExists   = errors.New("that range already exists in the pool")
)

func (s *IPPoolStore) load() ([]IPPool, error) {
	var pools []IPPool
	err := readJSON(s.path, &pools)
	return pools, err
}

func (s *IPPoolStore) save(pools []IPPool) error {
	return writeJSON(s.path, pools)
}

// List returns all pools.
func (s *IPPoolStore) List() ([]IPPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Get resolves a pool by id or name.
func (s *IPPoolStore) Get(ref string) (IPPool, bool, error) {
	pools, err := s.List()
	if err != nil {
		return IPPool{}, false, err
	}
	for _, p := range pools {
		if p.ID == ref || p.Name == ref {
			return p, true, nil
		}
	}
	return IPPool{}, false, nil
}

// Create stores a new pool, rejecting a duplicate name. If it is the first pool
// of its (pool_type, ip_version) it becomes the default for that combination.
func (s *IPPoolStore) Create(p IPPool) (IPPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return IPPool{}, err
	}
	hasDefault := false
	for _, e := range pools {
		if e.Name == p.Name {
			return IPPool{}, ErrPoolNameTaken
		}
		if e.IsDefault && e.PoolType == p.PoolType && e.IPVersion == p.IPVersion {
			hasDefault = true
		}
	}
	if !hasDefault {
		p.IsDefault = true
	}
	if p.Ranges == nil {
		p.Ranges = []IPRange{}
	}
	pools = append(pools, p)
	return p, s.save(pools)
}

// Update mutates name/description. Nil pointers leave a field unchanged.
func (s *IPPoolStore) Update(ref string, name, desc *string, now time.Time) (IPPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return IPPool{}, false, err
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
	return IPPool{}, false, nil
}

// Delete removes a pool by id/name.
func (s *IPPoolStore) Delete(ref string) (bool, error) {
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

// AddRange appends a range to a pool.
func (s *IPPoolStore) AddRange(ref, first, last, rangeID string, now time.Time) (IPPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return IPPool{}, false, err
	}
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			for _, rg := range pools[i].Ranges {
				if rg.First == first && rg.Last == last {
					return IPPool{}, false, ErrRangeExists
				}
			}
			pools[i].Ranges = append(pools[i].Ranges, IPRange{ID: rangeID, First: first, Last: last, TimeCreated: now})
			pools[i].TimeModified = now
			return pools[i], true, s.save(pools)
		}
	}
	return IPPool{}, false, nil
}

// RemoveRange drops a range (matched by first+last) from a pool.
func (s *IPPoolStore) RemoveRange(ref, first, last string, now time.Time) (IPPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return IPPool{}, false, err
	}
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			out := pools[i].Ranges[:0]
			for _, rg := range pools[i].Ranges {
				if rg.First == first && rg.Last == last {
					continue
				}
				out = append(out, rg)
			}
			pools[i].Ranges = out
			pools[i].TimeModified = now
			return pools[i], true, s.save(pools)
		}
	}
	return IPPool{}, false, nil
}

// SetDefault marks a pool default (clearing other defaults of the same
// pool_type + ip_version) or clears the flag.
func (s *IPPoolStore) SetDefault(ref string, isDefault bool, now time.Time) (IPPool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return IPPool{}, false, err
	}
	idx := -1
	for i := range pools {
		if pools[i].ID == ref || pools[i].Name == ref {
			idx = i
			break
		}
	}
	if idx == -1 {
		return IPPool{}, false, nil
	}
	if isDefault {
		for i := range pools {
			if pools[i].PoolType == pools[idx].PoolType && pools[i].IPVersion == pools[idx].IPVersion {
				pools[i].IsDefault = false
			}
		}
	}
	pools[idx].IsDefault = isDefault
	pools[idx].TimeModified = now
	return pools[idx], true, s.save(pools)
}

// SeedIfEmpty creates an initial pool when the store has none, so existing
// floating-IP behavior (a single default range) carries over.
func (s *IPPoolStore) SeedIfEmpty(p IPPool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools, err := s.load()
	if err != nil {
		return err
	}
	if len(pools) > 0 {
		return nil
	}
	if p.Ranges == nil {
		p.Ranges = []IPRange{}
	}
	p.IsDefault = true
	return s.save([]IPPool{p})
}

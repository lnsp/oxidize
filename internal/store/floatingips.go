package store

import (
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// FloatingIP is a persisted floating-IP allocation. InstanceVMID is 0 when the
// address is detached.
type FloatingIP struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	IP           string    `json:"ip"`
	PoolID       string    `json:"pool_id"`
	ProjectID    string    `json:"project_id"`
	InstanceVMID int       `json:"instance_vmid"`
	Ephemeral    bool      `json:"ephemeral"` // instance-bound, auto-released on delete
	TimeCreated  time.Time `json:"time_created"`
	TimeModified time.Time `json:"time_modified"`
}

// FloatingIPStore persists floating-IP allocations as a JSON file.
type FloatingIPStore struct {
	path string
	mu   sync.Mutex
}

// NewFloatingIPStore returns a store backed by <dir>/floating-ips.json.
func NewFloatingIPStore(dir string) *FloatingIPStore {
	return &FloatingIPStore{path: filepath.Join(dir, "floating-ips.json")}
}

func (s *FloatingIPStore) load() ([]FloatingIP, error) {
	var fips []FloatingIP
	err := readJSON(s.path, &fips)
	return fips, err
}

func (s *FloatingIPStore) save(fips []FloatingIP) error {
	return writeJSON(s.path, fips)
}

// List returns all floating IPs.
func (s *FloatingIPStore) List() ([]FloatingIP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Get resolves a floating IP by id, name, or address.
func (s *FloatingIPStore) Get(ref string) (FloatingIP, bool, error) {
	fips, err := s.List()
	if err != nil {
		return FloatingIP{}, false, err
	}
	for _, f := range fips {
		if f.ID == ref || f.Name == ref || f.IP == ref {
			return f, true, nil
		}
	}
	return FloatingIP{}, false, nil
}

// ErrNameTaken / ErrIPTaken are returned by Create on a conflict; ErrNoFreeIP
// when an allocator finds no free address.
var (
	ErrNameTaken = errors.New("a floating IP with that name already exists")
	ErrIPTaken   = errors.New("that address is already allocated")
	ErrNoFreeIP  = errors.New("no free addresses in the IP pool")
)

// Create stores a new floating IP, rejecting duplicate names or addresses.
func (s *FloatingIPStore) Create(fip FloatingIP) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fips, err := s.load()
	if err != nil {
		return err
	}
	for _, f := range fips {
		if f.Name == fip.Name {
			return ErrNameTaken
		}
		if f.IP == fip.IP {
			return ErrIPTaken
		}
	}
	fips = append(fips, fip)
	return s.save(fips)
}

// CreateAllocated atomically chooses and persists a floating IP. pick is called
// under the store lock with the set of addresses already in use and returns the
// address to assign rec ("" if none are free). Selecting and persisting in one
// critical section prevents concurrent auto-allocations from picking the same
// address. rec.IP is ignored (pick supplies it).
func (s *FloatingIPStore) CreateAllocated(rec FloatingIP, pick func(used map[string]bool) string) (FloatingIP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fips, err := s.load()
	if err != nil {
		return FloatingIP{}, err
	}
	used := make(map[string]bool, len(fips))
	for _, f := range fips {
		if f.Name == rec.Name {
			return FloatingIP{}, ErrNameTaken
		}
		used[f.IP] = true
	}
	ip := pick(used)
	if ip == "" {
		return FloatingIP{}, ErrNoFreeIP
	}
	if used[ip] {
		return FloatingIP{}, ErrIPTaken
	}
	rec.IP = ip
	fips = append(fips, rec)
	return rec, s.save(fips)
}

// Delete removes a floating IP by id/name/ip. Returns true if one was removed.
func (s *FloatingIPStore) Delete(ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fips, err := s.load()
	if err != nil {
		return false, err
	}
	out := fips[:0]
	removed := false
	for _, f := range fips {
		if f.ID == ref || f.Name == ref || f.IP == ref {
			removed = true
			continue
		}
		out = append(out, f)
	}
	if !removed {
		return false, nil
	}
	return true, s.save(out)
}

// SetAttachment sets (vmid > 0) or clears (vmid == 0) the attached instance for
// a floating IP, returning the updated record.
func (s *FloatingIPStore) SetAttachment(ref string, vmid int, now time.Time) (FloatingIP, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fips, err := s.load()
	if err != nil {
		return FloatingIP{}, false, err
	}
	for i := range fips {
		if fips[i].ID == ref || fips[i].Name == ref || fips[i].IP == ref {
			fips[i].InstanceVMID = vmid
			fips[i].TimeModified = now
			updated := fips[i]
			return updated, true, s.save(fips)
		}
	}
	return FloatingIP{}, false, nil
}

// UsedIPs returns the set of allocated addresses (for allocation).
func (s *FloatingIPStore) UsedIPs() (map[string]bool, error) {
	fips, err := s.List()
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool, len(fips))
	for _, f := range fips {
		used[f.IP] = true
	}
	return used, nil
}

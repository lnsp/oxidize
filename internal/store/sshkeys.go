// Package store provides small file-backed persistence for state that has no
// Proxmox equivalent (e.g. the synthetic user's SSH keys).
package store

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

// SSHKeyStore persists the synthetic user's SSH keys as a JSON file.
type SSHKeyStore struct {
	path string
	mu   sync.Mutex
}

// NewSSHKeyStore returns a store backed by <dir>/ssh-keys.json.
func NewSSHKeyStore(dir string) *SSHKeyStore {
	return &SSHKeyStore{path: filepath.Join(dir, "ssh-keys.json")}
}

func (s *SSHKeyStore) load() ([]oxide.SshKey, error) {
	var keys []oxide.SshKey
	err := readJSON(s.path, &keys)
	return keys, err
}

func (s *SSHKeyStore) save(keys []oxide.SshKey) error {
	return writeJSON(s.path, keys)
}

// List returns all stored keys.
func (s *SSHKeyStore) List() ([]oxide.SshKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Get returns a key by id or name.
func (s *SSHKeyStore) Get(ref string) (oxide.SshKey, bool, error) {
	keys, err := s.List()
	if err != nil {
		return oxide.SshKey{}, false, err
	}
	for _, k := range keys {
		if k.ID == ref || k.Name == ref {
			return k, true, nil
		}
	}
	return oxide.SshKey{}, false, nil
}

// Add stores a new key (or returns the existing one if the name is taken).
func (s *SSHKeyStore) Add(name, description, publicKey string, now time.Time) (oxide.SshKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.load()
	if err != nil {
		return oxide.SshKey{}, err
	}
	for _, k := range keys {
		if k.Name == name {
			return k, nil
		}
	}
	key := oxide.SshKey{
		ID:           translate.UUIDv5("sshkey:" + name),
		Name:         name,
		Description:  description,
		PublicKey:    publicKey,
		SiloUserID:   translate.UserID,
		TimeCreated:  now,
		TimeModified: now,
	}
	keys = append(keys, key)
	if err := s.save(keys); err != nil {
		return oxide.SshKey{}, err
	}
	return key, nil
}

// Delete removes a key by id or name. Returns true if a key was removed.
func (s *SSHKeyStore) Delete(ref string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.load()
	if err != nil {
		return false, err
	}
	out := keys[:0]
	removed := false
	for _, k := range keys {
		if k.ID == ref || k.Name == ref {
			removed = true
			continue
		}
		out = append(out, k)
	}
	if !removed {
		return false, nil
	}
	return true, s.save(out)
}

// PublicKeys returns just the public-key strings (for cloud-init injection).
func (s *SSHKeyStore) PublicKeys() []string {
	keys, err := s.List()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.PublicKey)
	}
	return out
}

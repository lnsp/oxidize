package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// readJSON loads a JSON-array store file into *out. A missing or empty file is
// treated as an empty set (out is left unchanged). A file that exists but does
// not parse is NOT silently discarded: it is renamed to <path>.corrupt and an
// error is returned, so a truncated or garbled file can't be quietly
// overwritten with empty state on the next save. Combined with writeJSON's
// atomic rename, this means good data survives crashes and operator-visible
// corruption is preserved for recovery rather than lost.
func readJSON[T any](path string, out *[]T) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil // an empty file is an empty set, not corruption
	}
	if err := json.Unmarshal(b, out); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return fmt.Errorf("store %s was corrupt (backed up to %s.corrupt): %w", path, path, err)
	}
	return nil
}

// writeJSON atomically persists a JSON-array store file. It marshals v, writes
// it to a temp file in the same directory, fsyncs, then renames it over path.
// The rename is atomic on a POSIX filesystem, so a crash mid-write leaves the
// previous file intact instead of truncated.
func writeJSON[T any](path string, v []T) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless no-op once the rename succeeds
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

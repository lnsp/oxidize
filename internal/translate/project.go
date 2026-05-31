package translate

import (
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
)

// DefaultProjectName is the project that holds VMs not in any Proxmox pool.
const DefaultProjectName = "proxmox"

// PoolIDFromName derives a valid PVE poolid from an Oxide project name. PVE
// constrains poolids to [A-Za-z0-9.\-_]; an Oxide Name (lowercase [a-z0-9-],
// letter-initial) is already a subset, so the only coercion needed is rejecting
// the empty/all-invalid case. Returns ok=false when no valid poolid can be
// formed (e.g. an empty name).
func PoolIDFromName(name string) (poolid string, ok bool) {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" {
		return "", false
	}
	return s, true
}

// ProjectIDForPool returns the Oxide project id for a Proxmox pool. The empty
// pool ("" — i.e. unpooled VMs) maps to the default project.
func ProjectIDForPool(pool string) string {
	if pool == "" {
		return ProjectID
	}
	return UUIDv5("project:pool:" + pool)
}

// ProjectFromPool builds the Oxide project for a Proxmox pool (or the default
// project when pool is "").
func ProjectFromPool(pool, comment string) oxide.Project {
	if pool == "" {
		return oxide.Project{
			ID:           ProjectID,
			Name:         DefaultProjectName,
			Description:  "VMs not in a Proxmox resource pool",
			TimeCreated:  epoch,
			TimeModified: epoch,
		}
	}
	desc := comment
	if desc == "" {
		desc = "Proxmox resource pool " + pool
	}
	return oxide.Project{
		ID:           ProjectIDForPool(pool),
		Name:         SanitizeName(pool, "pool"),
		Description:  desc,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

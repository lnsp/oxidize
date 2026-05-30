package translate

import (
	"github.com/lennart/oxidize/internal/oxide"
)

// DefaultProjectName is the project that holds VMs not in any Proxmox pool.
const DefaultProjectName = "proxmox"

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

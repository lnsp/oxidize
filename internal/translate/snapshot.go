package translate

import (
	"strconv"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
)

// DiskPoolVMID is the reserved Proxmox VMID namespace under which oxidize
// allocates standalone (project-level, unattached) disks, since Proxmox has no
// first-class concept of a disk not owned by a VM.
const DiskPoolVMID = 9000

// SnapshotID derives a stable snapshot UUID from a vmid and snapshot name.
func SnapshotID(vmid int, name string) string {
	return UUIDv5("snapshot:" + strconv.Itoa(vmid) + ":" + name)
}

// SnapshotFromProxmox maps a Proxmox VM snapshot to an Oxide Snapshot. Proxmox
// snapshots are VM-level; we associate them with the VM's boot disk.
func SnapshotFromProxmox(vmid int, diskID string, sizeBytes int64, s proxmox.QemuSnapshot) oxide.Snapshot {
	created := epoch
	if s.SnapTime > 0 {
		created = time.Unix(s.SnapTime, 0).UTC()
	}
	return oxide.Snapshot{
		ID:           SnapshotID(vmid, s.Name),
		Name:         SanitizeName(s.Name, "snapshot"),
		Description:  s.Description,
		DiskID:       diskID,
		ProjectID:    ProjectID,
		Size:         sizeBytes,
		State:        "ready",
		TimeCreated:  created,
		TimeModified: created,
	}
}

// VolumeDiskID derives a stable disk UUID for a standalone storage volume.
func VolumeDiskID(volid string) string { return UUIDv5("vol:" + volid) }

// StandaloneDisk maps an unattached storage volume to a detached Oxide Disk.
func StandaloneDisk(volid string, sizeBytes int64) oxide.Disk {
	name := volid
	if _, after, ok := cut(volid, ':'); ok {
		name = after
	}
	return oxide.Disk{
		ID:           VolumeDiskID(volid),
		Name:         SanitizeName(name, "disk"),
		Description:  "Proxmox volume " + volid,
		BlockSize:    512,
		DevicePath:   "/dev/disk/" + SanitizeName(name, "disk"),
		DiskType:     "distributed",
		ProjectID:    ProjectID,
		ReadOnly:     false,
		Size:         sizeBytes,
		State:        oxide.Detached(),
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

func cut(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

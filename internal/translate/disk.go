package translate

import (
	"strconv"
	"strings"

	"github.com/lennart/oxidize/internal/oxide"
)

// diskDevicePrefixes are the QEMU config keys that describe attached disks.
var diskDevicePrefixes = []string{"scsi", "virtio", "sata", "ide"}

// DisksFromConfig extracts attached VM disks from a QEMU config map and renders
// them as Oxide Disks attached to the given instance. CD-ROM media entries are
// skipped (they're images, not disks).
func DisksFromConfig(vmid int, instanceID, projectID string, cfg map[string]string) []oxide.Disk {
	var disks []oxide.Disk
	for _, prefix := range diskDevicePrefixes {
		for i := 0; i < 16; i++ {
			dev := prefix + strconv.Itoa(i)
			val, ok := cfg[dev]
			if !ok || strings.Contains(val, "media=cdrom") {
				continue
			}
			disks = append(disks, diskFromEntry(vmid, instanceID, projectID, dev, val))
		}
	}
	return disks
}

func diskFromEntry(vmid int, instanceID, projectID, dev, val string) oxide.Disk {
	size := parseDiskSize(val)
	return oxide.Disk{
		ID:           DiskID(vmid, dev),
		Name:         SanitizeName("vm-"+strconv.Itoa(vmid)+"-"+dev, "disk"),
		Description:  "Proxmox disk " + dev,
		BlockSize:    512,
		DevicePath:   "/dev/" + dev,
		DiskType:     "distributed",
		ImageID:      nil,
		SnapshotID:   nil,
		ProjectID:    projectID,
		ReadOnly:     false,
		Size:         size,
		State:        oxide.Attached(instanceID),
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// parseDiskSize pulls "size=NN[KMGT]" out of a disk config value and returns
// the size in bytes. Returns 0 if absent.
func parseDiskSize(val string) int64 {
	for _, part := range strings.Split(val, ",") {
		if k, v, ok := strings.Cut(part, "="); ok && k == "size" {
			return parseHumanBytes(v)
		}
	}
	return 0
}

// parseHumanBytes parses values like "32G", "512M", "1024" into bytes.
func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch last := s[len(s)-1]; last {
	case 'K', 'k':
		mult, s = 1024, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1024*1024, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1024*1024*1024, s[:len(s)-1]
	case 'T', 't':
		mult, s = 1024*1024*1024*1024, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(n * float64(mult))
}

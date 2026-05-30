package translate

import (
	"strconv"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/proxmox"
)

// SledFromNode maps a Proxmox cluster node to an Oxide Sled.
func SledFromNode(n proxmox.Node) oxide.Sled {
	return oxide.Sled{
		ID: SledID(n.Node),
		Baseboard: oxide.Baseboard{
			Part:     "proxmox-node",
			Revision: 0,
			Serial:   n.Node,
		},
		Policy:                oxide.SledPolicy{Kind: "in_service", ProvisionPolicy: "provisionable"},
		RackID:                RackID,
		State:                 "active",
		UsableHardwareThreads: n.MaxCPU,
		UsablePhysicalRAM:     n.MaxMem,
		TimeCreated:           epoch,
		TimeModified:          epoch,
	}
}

// PhysicalDiskID derives a stable disk UUID from its serial number.
func PhysicalDiskID(serial string) string { return UUIDv5("pdisk:" + serial) }

// PhysicalDiskFromNode maps a Proxmox node disk to an Oxide PhysicalDisk.
func PhysicalDiskFromNode(node string, d proxmox.NodeDisk) oxide.PhysicalDisk {
	form := "u2"
	if d.Type == "nvme" {
		form = "m2"
	}
	vendor := d.Vendor
	if vendor == "" || vendor == "unknown" {
		vendor = "unknown"
	}
	sledID := SledID(node)
	return oxide.PhysicalDisk{
		ID:           PhysicalDiskID(d.Serial),
		FormFactor:   form,
		Model:        d.Model,
		Policy:       oxide.PhysicalDiskPolicy{Kind: "in_service"},
		Serial:       d.Serial,
		SledID:       &sledID,
		State:        "active",
		Vendor:       vendor,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// SledInstanceFromResource maps a VM on a node to a SledInstance.
func SledInstanceFromResource(node string, r proxmox.ClusterResource) oxide.SledInstance {
	name := SanitizeName(r.Name, "vm-"+strconv.Itoa(r.VMID))
	return oxide.SledInstance{
		ID:           InstanceID(r.VMID),
		ActiveSledID: SledID(node),
		Memory:       r.MaxMem,
		Name:         name,
		NCPUs:        r.MaxCPU,
		ProjectName:  "proxmox",
		SiloName:     "proxmox",
		State:        RunState(r.Status, "", r.Lock),
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

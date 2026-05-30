package translate

import (
	"strconv"
	"strings"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
)

// epoch is a stable synthetic creation timestamp. Proxmox doesn't expose a VM
// creation time, and a stable value avoids spurious UI churn on each poll.
var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// RunState maps Proxmox status/qmpstatus/lock to an Oxide InstanceState.
func RunState(status, qmpstatus, lock string) oxide.InstanceState {
	switch lock {
	case "migrate":
		return oxide.StateMigrating
	case "backup":
		return oxide.StateBackingUp
	}
	// Other locks (snapshot, rollback, clone, ...) don't have a faithful Oxide
	// state, so fall through to the VM's actual power status below rather than
	// inventing an alarming "repairing".
	switch qmpstatus {
	case "paused", "suspended", "prelaunch":
		return oxide.StateStopped
	}
	switch status {
	case "running":
		return oxide.StateRunning
	case "stopped":
		return oxide.StateStopped
	case "paused", "suspended":
		return oxide.StateStopped
	default:
		return oxide.StateStopped
	}
}

// InstanceFromResource builds an Oxide Instance from a cluster resource entry.
// This is enough for the list view; detail/config enrich it via Enrich.
func InstanceFromResource(r proxmox.ClusterResource) oxide.Instance {
	name := SanitizeName(r.Name, "vm-"+strconv.Itoa(r.VMID))
	return oxide.Instance{
		ID:                  InstanceID(r.VMID),
		Name:                name,
		Description:         vmidNote(r.VMID),
		Hostname:            name,
		ProjectID:           ProjectIDForPool(r.Pool),
		RunState:            RunState(r.Status, "", r.Lock),
		NCPUs:               r.MaxCPU,
		Memory:              r.MaxMem,
		BootDiskID:          nil,
		CPUPlatform:         nil,
		AutoRestartEnabled:  false,
		AutoRestartPolicy:   nil,
		TimeCreated:         epoch,
		TimeModified:        epoch,
		TimeRunStateUpdated: time.Now().UTC(),
	}
}

// ApplyStatus refines an instance's run state from a live status query.
func ApplyStatus(inst *oxide.Instance, st *proxmox.QemuStatus) {
	if st == nil {
		return
	}
	inst.RunState = RunState(st.Status, st.QmpStatus, st.Lock)
	if st.MaxMem > 0 {
		inst.Memory = st.MaxMem
	}
	if st.CPUs > 0 {
		inst.NCPUs = st.CPUs
	}
}

// ApplyConfig refines an instance from its config map: cpu count (cores*sockets),
// memory (MiB), hostname, and boot disk id.
func ApplyConfig(inst *oxide.Instance, vmid int, cfg map[string]string) {
	cores := atoiDefault(cfg["cores"], 1)
	sockets := atoiDefault(cfg["sockets"], 1)
	if cores*sockets > 0 {
		inst.NCPUs = cores * sockets
	}
	if mib := atoiDefault(cfg["memory"], 0); mib > 0 {
		inst.Memory = int64(mib) * 1024 * 1024
	}
	if name := cfg["name"]; name != "" {
		inst.Hostname = SanitizeName(name, inst.Name)
	}
	// boot disk: prefer explicit bootdisk, else first attached disk device.
	dev := cfg["bootdisk"]
	if dev == "" {
		dev = firstDiskDevice(cfg)
	}
	if dev != "" {
		id := DiskID(vmid, dev)
		inst.BootDiskID = &id
	}
}

// firstDiskDevice returns the first VM disk device key present in a config.
func firstDiskDevice(cfg map[string]string) string {
	for _, prefix := range []string{"scsi", "virtio", "sata", "ide"} {
		for i := 0; i < 16; i++ {
			k := prefix + strconv.Itoa(i)
			if v, ok := cfg[k]; ok && !strings.Contains(v, "media=cdrom") {
				return k
			}
		}
	}
	return ""
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func vmidNote(vmid int) string { return "Proxmox VM " + strconv.Itoa(vmid) }

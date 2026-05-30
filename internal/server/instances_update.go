package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/translate"
)

type instanceUpdateBody struct {
	BootDisk *string `json:"boot_disk"`
	NCPUs    int     `json:"ncpus"`
	Memory   int64   `json:"memory"`
}

// handleInstanceUpdate maps Oxide InstanceUpdate onto a Proxmox config edit
// (vCPUs, memory, and boot disk order). Changes to a running VM take effect on
// next boot unless hotplug is enabled.
func (s *Server) handleInstanceUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	var body instanceUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	form := url.Values{}
	if body.NCPUs > 0 {
		form.Set("cores", strconv.Itoa(body.NCPUs))
	}
	if mib := body.Memory / (1024 * 1024); mib >= 16 {
		form.Set("memory", strconv.FormatInt(mib, 10))
	}
	if body.BootDisk != nil {
		if dev := diskDevice(ref.vmid, cfg, *body.BootDisk); dev != "" {
			form.Set("boot", "order="+dev)
		}
	}
	if len(form) > 0 {
		if _, err := s.pve.UpdateConfig(ctx, ref.node, ref.vmid, form); err != nil {
			writeProxmoxError(w, err)
			return
		}
	}
	oxide.WriteJSON(w, http.StatusOK, s.instanceDetail(ctx, ref))
}

type diskPathBody struct {
	Disk string `json:"disk"`
}

// handleDiskAttach attaches a standalone pool disk to the instance on the next
// free SCSI slot.
func (s *Server) handleDiskAttach(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	var body diskPathBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	volid := s.resolveDiskVolid(ctx, body.Disk)
	if volid == "" {
		oxide.WriteError(w, http.StatusBadRequest, "attachable disk not found: "+body.Disk)
		return
	}
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	dev := nextFreeIndexed(cfg, "scsi")
	form := url.Values{}
	form.Set(dev, volid)
	if _, err := s.pve.UpdateConfig(ctx, ref.node, ref.vmid, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	disk := translate.StandaloneDisk(volid, 0)
	disk.State = oxide.Attached(translate.InstanceID(ref.vmid))
	oxide.WriteJSON(w, http.StatusAccepted, disk)
}

// handleDiskDetach removes a disk's bus assignment; Proxmox preserves the
// volume as "unused" rather than destroying it.
func (s *Server) handleDiskDetach(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref, err := s.resolveInstance(ctx, r.PathValue("instance"))
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}
	var body diskPathBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := s.pve.QemuConfig(ctx, ref.node, ref.vmid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	dev := diskDevice(ref.vmid, cfg, body.Disk)
	if dev == "" {
		oxide.WriteError(w, http.StatusBadRequest, "disk not attached to this instance: "+body.Disk)
		return
	}
	form := url.Values{}
	form.Set("delete", dev)
	if _, err := s.pve.UpdateConfig(ctx, ref.node, ref.vmid, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	disk := translate.StandaloneDisk(volidOf(cfg[dev]), 0)
	disk.State = oxide.Detached()
	oxide.WriteJSON(w, http.StatusAccepted, disk)
}

// diskDevice finds the Proxmox device key (scsi0, ...) for a disk NameOrId.
func diskDevice(vmid int, cfg map[string]string, ref string) string {
	for _, d := range translate.DisksFromConfig(vmid, translate.InstanceID(vmid), translate.ProjectID, cfg) {
		if d.ID == ref || d.Name == ref {
			// d.DevicePath is "/dev/<dev>"
			return d.DevicePath[len("/dev/"):]
		}
	}
	return ""
}

// resolveDiskVolid maps a disk NameOrId to a Proxmox volid (pool disks).
func (s *Server) resolveDiskVolid(ctx context.Context, ref string) string {
	for _, v := range mustPool(s.poolVolumes(ctx)) {
		if translate.VolumeDiskID(v.VolID) == ref || matchesVolName(v.VolID, ref) {
			return v.VolID
		}
	}
	return ""
}

func mustPool(v []poolVolume, _ error) []poolVolume { return v }

// volidOf extracts the volid from a disk config value like "local-lvm:vm-1-disk-0,size=8G".
func volidOf(val string) string {
	for i := 0; i < len(val); i++ {
		if val[i] == ',' {
			return val[:i]
		}
	}
	return val
}

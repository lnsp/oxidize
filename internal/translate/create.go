package translate

import (
	"net/url"
	"strconv"
)

// InstanceCreateParams is the decoded subset of the Oxide InstanceCreate body
// that we can map onto a Proxmox VM.
type InstanceCreateParams struct {
	Name        string
	Hostname    string
	Description string
	NCPUs       int
	Memory      int64 // bytes
	Start       *bool
	BootDisk    *DiskAttachment
	Disks       []DiskAttachment
}

// DiskAttachment mirrors the Oxide InstanceDiskAttachment union (create/attach).
type DiskAttachment struct {
	Type    string // "create" | "attach"
	Name    string
	Size    int64  // bytes (create)
	ImageID string // disk_backend.disk_source.image_id when sourced from an image
}

// QemuCreateOptions carries server-resolved placement details.
type QemuCreateOptions struct {
	VMID       int
	Storage    string // images-capable storage for the boot disk
	Bridge     string // network bridge for net0
	ISOVolid   string // optional ISO to attach as a cdrom
	BootSizeGB int    // boot disk size in GiB (already resolved)
}

// QemuCreateForm builds the form-encoded body for POST /nodes/{node}/qemu.
func QemuCreateForm(p InstanceCreateParams, o QemuCreateOptions) url.Values {
	form := url.Values{}
	form.Set("vmid", strconv.Itoa(o.VMID))
	// Proxmox VM name must be a DNS-style name; the Oxide name already is.
	name := p.Name
	if name == "" {
		name = p.Hostname
	}
	if name != "" {
		form.Set("name", SanitizeName(name, "vm-"+strconv.Itoa(o.VMID)))
	}
	cores := p.NCPUs
	if cores < 1 {
		cores = 1
	}
	form.Set("cores", strconv.Itoa(cores))
	form.Set("sockets", "1")
	mib := p.Memory / (1024 * 1024)
	if mib < 16 {
		mib = 512 // sane floor
	}
	form.Set("memory", strconv.FormatInt(mib, 10))
	form.Set("scsihw", "virtio-scsi-single")
	form.Set("scsi0", o.Storage+":"+strconv.Itoa(o.BootSizeGB))
	form.Set("net0", "virtio,bridge="+o.Bridge)
	form.Set("serial0", "socket") // enables the serial console bridge
	form.Set("ostype", "l26")
	if o.ISOVolid != "" {
		form.Set("ide2", o.ISOVolid+",media=cdrom")
		form.Set("boot", "order=scsi0;ide2")
	} else {
		form.Set("boot", "order=scsi0")
	}
	if p.Description != "" {
		form.Set("description", p.Description)
	}
	return form
}

// BootSizeGB resolves the requested boot-disk size in GiB (rounded up) from the
// create params, falling back to a default when no create-disk is specified.
func BootSizeGB(p InstanceCreateParams) int {
	bytes := int64(0)
	if p.BootDisk != nil && p.BootDisk.Type == "create" {
		bytes = p.BootDisk.Size
	}
	if bytes == 0 {
		for _, d := range p.Disks {
			if d.Type == "create" && d.Size > 0 {
				bytes = d.Size
				break
			}
		}
	}
	if bytes <= 0 {
		return 8 // default 8 GiB
	}
	gib := int((bytes + (1024*1024*1024 - 1)) / (1024 * 1024 * 1024))
	if gib < 1 {
		gib = 1
	}
	return gib
}

// ImageRef returns the first image_id referenced by the boot disk or any
// create-disk, or "" if none.
func ImageRef(p InstanceCreateParams) string {
	if p.BootDisk != nil && p.BootDisk.ImageID != "" {
		return p.BootDisk.ImageID
	}
	for _, d := range p.Disks {
		if d.ImageID != "" {
			return d.ImageID
		}
	}
	return ""
}

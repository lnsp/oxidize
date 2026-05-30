package translate

import (
	"path"
	"strconv"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
)

func itoa(n int) string { return strconv.Itoa(n) }

// ImageFromContent maps a Proxmox storage volume (an ISO or container template)
// to an Oxide Image. The volid looks like "local:iso/debian-12.iso".
func ImageFromContent(c proxmox.StorageContent) oxide.Image {
	base := c.VolID
	if _, after, ok := strings.Cut(c.VolID, ":"); ok {
		base = after
	}
	filename := path.Base(base)
	name := SanitizeName(strings.TrimSuffix(filename, path.Ext(filename)), "image")
	return oxide.Image{
		ID:           ImageID(c.VolID),
		Name:         name,
		Description:  c.VolID,
		OS:           guessOS(filename),
		Version:      "1",
		BlockSize:    512,
		Size:         c.Size,
		Digest:       nil,
		ProjectID:    nil,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// TemplateImageID derives an image UUID for a Proxmox VM template (by vmid).
func TemplateImageID(vmid int) string { return UUIDv5("template:" + itoa(vmid)) }

// ImageFromTemplate maps a Proxmox VM template to an Oxide Image. Cloning this
// image when creating an instance produces a working VM.
func ImageFromTemplate(e proxmox.QemuListEntry) oxide.Image {
	name := SanitizeName(e.Name, "template-"+itoa(e.VMID))
	return oxide.Image{
		ID:           TemplateImageID(e.VMID),
		Name:         name,
		Description:  "Proxmox template " + itoa(e.VMID),
		OS:           guessOS(e.Name),
		Version:      "1",
		BlockSize:    512,
		Size:         e.MaxDisk,
		Digest:       nil,
		ProjectID:    nil,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// guessOS makes a best-effort family guess from a filename.
func guessOS(filename string) string {
	f := strings.ToLower(filename)
	switch {
	case strings.Contains(f, "debian"):
		return "Debian"
	case strings.Contains(f, "ubuntu"):
		return "Ubuntu"
	case strings.Contains(f, "alpine"):
		return "Alpine"
	case strings.Contains(f, "centos"):
		return "CentOS"
	case strings.Contains(f, "fedora"):
		return "Fedora"
	case strings.Contains(f, "rocky"):
		return "Rocky"
	case strings.Contains(f, "win"):
		return "Windows"
	default:
		return "unknown"
	}
}

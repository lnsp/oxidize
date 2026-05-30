package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

// instanceCreateBody is the decoded subset of the Oxide InstanceCreate request.
// The wire format is snake_case.
type instanceCreateBody struct {
	Name        string           `json:"name"`
	Hostname    string           `json:"hostname"`
	Description string           `json:"description"`
	NCPUs       int              `json:"ncpus"`
	Memory      int64            `json:"memory"`
	Start       *bool            `json:"start"`
	BootDisk    *diskAttachBody  `json:"boot_disk"`
	Disks       []diskAttachBody `json:"disks"`
}

type diskAttachBody struct {
	Type        string `json:"type"` // "create" | "attach"
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DiskBackend *struct {
		Type       string `json:"type"`
		DiskSource *struct {
			Type    string `json:"type"`
			ImageID string `json:"image_id"`
		} `json:"disk_source"`
	} `json:"disk_backend"`
}

func (b diskAttachBody) toAttachment() translate.DiskAttachment {
	a := translate.DiskAttachment{Type: b.Type, Name: b.Name, Size: b.Size}
	if b.DiskBackend != nil && b.DiskBackend.DiskSource != nil {
		a.ImageID = b.DiskBackend.DiskSource.ImageID
	}
	return a
}

func (s *Server) handleInstanceCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body instanceCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := translate.InstanceCreateParams{
		Name:        body.Name,
		Hostname:    body.Hostname,
		Description: body.Description,
		NCPUs:       body.NCPUs,
		Memory:      body.Memory,
		Start:       body.Start,
	}
	if body.BootDisk != nil {
		a := body.BootDisk.toAttachment()
		params.BootDisk = &a
	}
	for _, d := range body.Disks {
		params.Disks = append(params.Disks, d.toAttachment())
	}

	vmid, err := s.pve.NextID(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}

	// If the boot image is a Proxmox VM template (the normal way to provision
	// on Proxmox, e.g. a cloud-init template), clone it. Otherwise build a fresh
	// VM with a blank disk, optionally attaching a chosen ISO as a CD.
	var node string
	if tmpl := s.resolveTemplate(ctx, translate.ImageRef(params)); tmpl != nil {
		node = tmpl.node
		if err := s.createByClone(ctx, tmpl, vmid, params); err != nil {
			writeProxmoxError(w, err)
			return
		}
	} else {
		node, err = s.createBlank(ctx, vmid, params)
		if err != nil {
			writeProxmoxError(w, err)
			return
		}
	}

	// If created under a pool-backed project, add the VM to that pool so it
	// lands in the right project.
	if pool, scoped := s.projectPool(ctx, r.URL.Query().Get("project")); scoped && pool != "" {
		_ = s.pve.PoolAddVM(ctx, pool, vmid)
	}

	// Start unless explicitly told not to (Oxide defaults start=true).
	if body.Start == nil || *body.Start {
		if startUpid, serr := s.pve.QemuAction(ctx, node, vmid, "start"); serr == nil {
			_ = s.pve.PollTask(ctx, node, startUpid, pveTimeout)
		}
	}

	ref := &vmRef{node: node, vmid: vmid}
	oxide.WriteJSON(w, http.StatusCreated, s.instanceDetail(ctx, ref))
}

// createBlank provisions a fresh VM with a blank boot disk (and optional ISO).
func (s *Server) createBlank(ctx context.Context, vmid int, params translate.InstanceCreateParams) (string, error) {
	node, err := s.firstNode(ctx)
	if err != nil {
		return "", err
	}
	storage, err := s.imagesStorage(ctx, node)
	if err != nil {
		return "", err
	}
	opts := translate.QemuCreateOptions{
		VMID:       vmid,
		Storage:    storage,
		Bridge:     s.firstBridge(ctx, node),
		BootSizeGB: translate.BootSizeGB(params),
	}
	if imgID := translate.ImageRef(params); imgID != "" {
		if volid := s.resolveImageVolid(ctx, imgID); volid != "" {
			opts.ISOVolid = volid
		}
	}
	upid, err := s.pve.CreateQemu(ctx, node, translate.QemuCreateForm(params, opts))
	if err != nil {
		return "", err
	}
	if err := s.pve.PollTask(ctx, node, upid, pveTimeout); err != nil && err != context.DeadlineExceeded {
		return "", err
	}
	return node, nil
}

// createByClone clones a template to the new VMID, then applies the requested
// cores/memory/name. Disk size and cloud-init customization are inherited from
// the template (see limitations noted to the user).
func (s *Server) createByClone(ctx context.Context, tmpl *vmRef, vmid int, params translate.InstanceCreateParams) error {
	name := translate.SanitizeName(firstNonEmpty(params.Name, params.Hostname), "vm-"+strconv.Itoa(vmid))
	// Linked clone (fast) so we can answer the request promptly; falls back to
	// full clone if the storage rejects linked clones.
	upid, err := s.pve.CloneQemu(ctx, tmpl.node, tmpl.vmid, vmid, name, false)
	if err != nil {
		upid, err = s.pve.CloneQemu(ctx, tmpl.node, tmpl.vmid, vmid, name, true)
		if err != nil {
			return err
		}
	}
	if err := s.pve.PollTask(ctx, tmpl.node, upid, cloneTimeout); err != nil && err != context.DeadlineExceeded {
		return err
	}
	// Apply requested sizing on top of the cloned template.
	cfg := url.Values{}
	// Ensure a serial console device so oxidize's (serial-only) console works.
	// Cloud images already run a getty on ttyS0, so this makes the console work
	// out of the box even if the source template lacks serial0.
	cfg.Set("serial0", "socket")
	if params.NCPUs > 0 {
		cfg.Set("cores", strconv.Itoa(params.NCPUs))
	}
	if mib := params.Memory / (1024 * 1024); mib >= 16 {
		cfg.Set("memory", strconv.FormatInt(mib, 10))
	}
	// Inject the user's stored SSH keys via cloud-init (templates carry a
	// cloud-init drive). Oxide's null sshPublicKeys means "all user keys".
	// Proxmox requires the sshkeys value itself to be URL-encoded; the form
	// encoder then percent-encodes it again for transport, and Proxmox decodes
	// once back to the URL-encoded form it expects.
	if keys := s.keys.PublicKeys(); len(keys) > 0 {
		// Proxmox wants the sshkeys value FULLY percent-encoded (it rejects a
		// string with literal reserved chars as "invalid urlencoded string").
		// QueryEscape encodes everything but uses "+" for space, which Proxmox's
		// decoder treats literally — so swap those to %20. (PathEscape won't do:
		// it leaves "+", "=", "@" literal.) The form encoder re-escapes the
		// %-signs for transport; Proxmox decodes once back to this form.
		cfg.Set("sshkeys", strings.ReplaceAll(url.QueryEscape(strings.Join(keys, "\n")), "+", "%20"))
	}
	if len(cfg) > 0 {
		if _, err := s.pve.UpdateConfig(ctx, tmpl.node, vmid, cfg); err != nil {
			return err
		}
	}
	return nil
}

// resolveTemplate finds the Proxmox VM template an image id refers to, if any.
func (s *Server) resolveTemplate(ctx context.Context, imageID string) *vmRef {
	if imageID == "" {
		return nil
	}
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return nil
	}
	for _, n := range nodes {
		vms, err := s.pve.NodeQemu(ctx, n.Node)
		if err != nil {
			continue
		}
		for _, vm := range vms {
			if vm.Template == 1 && translate.TemplateImageID(vm.VMID) == imageID {
				return &vmRef{node: n.Node, vmid: vm.VMID}
			}
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// firstNode returns the first online node (or the first node if none report online).
func (s *Server) firstNode(ctx context.Context) (string, error) {
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Status == "online" {
			return n.Node, nil
		}
	}
	if len(nodes) > 0 {
		return nodes[0].Node, nil
	}
	return "", &proxmoxNoNodes{}
}

type proxmoxNoNodes struct{}

func (*proxmoxNoNodes) Error() string { return "no Proxmox nodes available" }

// imagesStorage returns the first active storage that can hold VM disk images.
func (s *Server) imagesStorage(ctx context.Context, node string) (string, error) {
	storages, err := s.pve.Storages(ctx, node)
	if err != nil {
		return "", err
	}
	for _, st := range storages {
		if strings.Contains(st.Content, "images") {
			return st.Storage, nil
		}
	}
	return "", &proxmoxNoNodes{} // reuse: signals "nothing suitable"
}

// firstBridge returns a usable network bridge, defaulting to vmbr0.
func (s *Server) firstBridge(ctx context.Context, node string) string {
	bridges, err := s.pve.Bridges(ctx, node)
	if err == nil && len(bridges) > 0 {
		return bridges[0].Iface
	}
	return "vmbr0"
}

// resolveImageVolid maps an Oxide image id to its Proxmox volid (ISO/template
// storage volumes).
func (s *Server) resolveImageVolid(ctx context.Context, imageID string) string {
	_, volid := s.resolveImageVol(ctx, imageID)
	return volid
}

// resolveImageVol maps an Oxide image id back to the node + volid it was derived
// from, by scanning storage content.
func (s *Server) resolveImageVol(ctx context.Context, imageID string) (node, volid string) {
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return "", ""
	}
	for _, n := range nodes {
		storages, err := s.pve.Storages(ctx, n.Node)
		if err != nil {
			continue
		}
		for _, st := range storages {
			for _, content := range []string{"iso", "vztmpl"} {
				if !strings.Contains(st.Content, content) {
					continue
				}
				vols, err := s.pve.StorageContent(ctx, n.Node, st.Storage, content)
				if err != nil {
					continue
				}
				for _, v := range vols {
					if translate.ImageID(v.VolID) == imageID {
						return n.Node, v.VolID
					}
				}
			}
		}
	}
	return "", ""
}

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
	Name              string           `json:"name"`
	Hostname          string           `json:"hostname"`
	Description       string           `json:"description"`
	NCPUs             int              `json:"ncpus"`
	Memory            int64            `json:"memory"`
	Start             *bool            `json:"start"`
	BootDisk          *diskAttachBody  `json:"boot_disk"`
	Disks             []diskAttachBody `json:"disks"`
	NetworkInterfaces *nicCreateUnion  `json:"network_interfaces"`
}

// nicCreateUnion is the InstanceNetworkInterfaceAttachment tagged union:
// {type:"default"} | {type:"none"} | {type:"create", params:[...]}.
type nicCreateUnion struct {
	Type   string `json:"type"`
	Params []struct {
		Name       string `json:"name"`
		VpcName    string `json:"vpc_name"`
		SubnetName string `json:"subnet_name"`
	} `json:"params"`
}

// primaryBridge resolves the bridge the instance's first NIC should attach to
// from the create request. When the request picks a network explicitly
// (type=="create"), that subnet's bridge wins. When it doesn't (the console's
// "Default" option, or nothing), the configured default subnet applies — this
// is what lets an SDN network be the default and the flat LAN opt-in. Returns
// "" to mean "leave the template/blank default bridge" (vmbr0).
func (s *Server) primaryBridge(ctx context.Context, nics *nicCreateUnion) string {
	if nics != nil {
		switch nics.Type {
		case "create":
			if len(nics.Params) > 0 {
				return s.sdnTopology(ctx).vnetBridge(nics.Params[0].SubnetName)
			}
		case "none":
			return ""
		}
	}
	if s.cfg.DefaultSubnet != "" {
		return s.sdnTopology(ctx).vnetBridge(s.cfg.DefaultSubnet)
	}
	return ""
}

// attachExtraNICs adds the second and subsequent requested NICs (net1, net2,
// ...) as fresh virtio interfaces on each one's subnet bridge. The first NIC is
// handled as net0 by the create path; a default-subnet NIC uses the flat LAN
// bridge.
func (s *Server) attachExtraNICs(ctx context.Context, node string, vmid int, nics *nicCreateUnion) {
	if nics == nil || nics.Type != "create" || len(nics.Params) < 2 {
		return
	}
	topo := s.sdnTopology(ctx)
	cfg, err := s.pve.QemuConfig(ctx, node, vmid)
	if err != nil {
		return
	}
	form := url.Values{}
	for _, p := range nics.Params[1:] {
		bridge := topo.vnetBridge(p.SubnetName)
		if bridge == "" {
			bridge = s.firstBridge(ctx, node)
		}
		dev := nextFreeIndexed(cfg, "net")
		val := "virtio,bridge=" + bridge
		cfg[dev] = val // reserve so the next iteration picks a higher index
		form.Set(dev, val)
		// Cloud-init only configures NICs that have a matching ipconfigN; without
		// this the guest leaves the extra interface down (no DHCP lease). The
		// template's primary NIC already carries ipconfig0.
		form.Set("ipconfig"+strings.TrimPrefix(dev, "net"), "ip=dhcp")
	}
	if len(form) > 0 {
		_, _ = s.pve.UpdateConfig(ctx, node, vmid, form)
	}
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
	bridge := s.primaryBridge(ctx, body.NetworkInterfaces)
	var node string
	if tmpl := s.resolveTemplate(ctx, translate.ImageRef(params)); tmpl != nil {
		node = tmpl.node
		if err := s.createByClone(ctx, tmpl, vmid, params, bridge); err != nil {
			writeProxmoxError(w, err)
			return
		}
	} else {
		node, err = s.createBlank(ctx, vmid, params, bridge)
		if err != nil {
			writeProxmoxError(w, err)
			return
		}
	}

	// Attach any additional requested NICs (the first maps to net0 above; the
	// rest become net1, net2, ... each on its subnet's bridge).
	s.attachExtraNICs(ctx, node, vmid, body.NetworkInterfaces)

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
// bridge selects the NIC's bridge ("" => the node's default bridge).
func (s *Server) createBlank(ctx context.Context, vmid int, params translate.InstanceCreateParams, bridge string) (string, error) {
	node, err := s.firstNode(ctx)
	if err != nil {
		return "", err
	}
	storage, err := s.imagesStorage(ctx, node)
	if err != nil {
		return "", err
	}
	if bridge == "" {
		bridge = s.firstBridge(ctx, node)
	}
	opts := translate.QemuCreateOptions{
		VMID:       vmid,
		Storage:    storage,
		Bridge:     bridge,
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
func (s *Server) createByClone(ctx context.Context, tmpl *vmRef, vmid int, params translate.InstanceCreateParams, bridge string) error {
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
	// Attach the primary NIC to the requested SDN bridge (vnet), preserving the
	// cloned NIC's model/MAC and just swapping the bridge.
	if bridge != "" {
		netval := "virtio,bridge=" + bridge
		if cur, cerr := s.pve.QemuConfig(ctx, tmpl.node, vmid); cerr == nil {
			netval = withBridge(cur["net0"], bridge)
		}
		cfg.Set("net0", netval)
	}
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

// withBridge rewrites the bridge= token of a Proxmox netN value (or appends one),
// keeping the model and MAC intact.
func withBridge(netval, bridge string) string {
	if netval == "" {
		return "virtio,bridge=" + bridge
	}
	parts := strings.Split(netval, ",")
	found := false
	for i, p := range parts {
		if strings.HasPrefix(p, "bridge=") {
			parts[i] = "bridge=" + bridge
			found = true
		}
	}
	if !found {
		parts = append(parts, "bridge="+bridge)
	}
	return strings.Join(parts, ",")
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

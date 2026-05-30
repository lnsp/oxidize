package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ClusterResources lists all resources, optionally filtered by type ("vm",
// "storage", "node", ...). An empty typ returns everything.
func (c *Client) ClusterResources(ctx context.Context, typ string) ([]ClusterResource, error) {
	path := "cluster/resources"
	if typ != "" {
		path += "?type=" + url.QueryEscape(typ)
	}
	var out []ClusterResource
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// QemuListEntry is one entry from GET /nodes/{node}/qemu. Unlike
// /cluster/resources, this reflects VM creation/deletion immediately.
type QemuListEntry struct {
	VMID     int    `json:"vmid"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	CPUs     int    `json:"cpus"`
	MaxMem   int64  `json:"maxmem"`
	MaxDisk  int64  `json:"maxdisk"`
	Uptime   int64  `json:"uptime"`
	Lock     string `json:"lock"`
	Template int    `json:"template"`
}

// NodeQemu lists the QEMU VMs on a node (current, not cache-delayed).
func (c *Client) NodeQemu(ctx context.Context, node string) ([]QemuListEntry, error) {
	var out []QemuListEntry
	if err := c.Get(ctx, "nodes/"+node+"/qemu", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Nodes lists cluster nodes.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var out []Node
	if err := c.Get(ctx, "nodes", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// QemuStatus fetches a VM's current status.
func (c *Client) QemuStatus(ctx context.Context, node string, vmid int) (*QemuStatus, error) {
	var out QemuStatus
	path := fmt.Sprintf("nodes/%s/qemu/%d/status/current", node, vmid)
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QemuConfig fetches a VM's config as a flat key/value map. Proxmox returns
// mixed value types (ints, strings), so values are normalized to strings.
func (c *Client) QemuConfig(ctx context.Context, node string, vmid int) (map[string]string, error) {
	var raw map[string]any
	path := fmt.Sprintf("nodes/%s/qemu/%d/config", node, vmid)
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = stringify(v)
	}
	return out, nil
}

// QemuAction performs a lifecycle action ("start","stop","shutdown","reboot",
// "reset") and returns the resulting UPID.
//
// For "shutdown" we attempt a graceful ACPI shutdown but pass forceStop=1 with a
// timeout so the VM is reliably powered off if the guest doesn't honor ACPI
// within the window. This matches the Oxide expectation that "stop" actually
// stops the instance, while still shutting down cleanly when the guest can.
func (c *Client) QemuAction(ctx context.Context, node string, vmid int, action string) (string, error) {
	path := fmt.Sprintf("nodes/%s/qemu/%d/status/%s", node, vmid, action)
	form := url.Values{}
	if action == "shutdown" {
		form.Set("forceStop", "1")
		form.Set("timeout", "60")
	}
	return c.PostForm(ctx, path, form)
}

// QemuDelete deletes a VM and returns the resulting UPID.
func (c *Client) QemuDelete(ctx context.Context, node string, vmid int) (string, error) {
	path := fmt.Sprintf("nodes/%s/qemu/%d", node, vmid)
	return c.Delete(ctx, path)
}

// TermProxyResult holds the credentials returned by the termproxy endpoint,
// needed to open the vncwebsocket stream.
type TermProxyResult struct {
	Ticket string
	Port   int
	User   string
	UPID   string
}

// TermProxy opens a serial/terminal proxy for a VM, returning the ticket+port
// needed to connect to the vncwebsocket endpoint.
func (c *Client) TermProxy(ctx context.Context, node string, vmid int) (*TermProxyResult, error) {
	path := fmt.Sprintf("nodes/%s/qemu/%d/termproxy", node, vmid)
	var raw map[string]any
	data, err := c.do(ctx, "POST", path, strings.NewReader(""), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		_ = json.Unmarshal(data, &raw)
	}
	port, _ := strconv.Atoi(stringify(raw["port"]))
	return &TermProxyResult{
		Ticket: stringify(raw["ticket"]),
		Port:   port,
		User:   stringify(raw["user"]),
		UPID:   stringify(raw["upid"]),
	}, nil
}

// Storages lists storages available on a node.
func (c *Client) Storages(ctx context.Context, node string) ([]Storage, error) {
	var out []Storage
	if err := c.Get(ctx, fmt.Sprintf("nodes/%s/storage", node), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StorageContent lists volumes of the given content type on a storage.
func (c *Client) StorageContent(ctx context.Context, node, storage, content string) ([]StorageContent, error) {
	path := fmt.Sprintf("nodes/%s/storage/%s/content", node, storage)
	if content != "" {
		path += "?content=" + url.QueryEscape(content)
	}
	var out []StorageContent
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// Proxmox numbers arrive as JSON numbers; render ints without a decimal.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", t)
	}
}

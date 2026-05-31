package proxmox

import (
	"context"
	"net/url"
	"strconv"
)

// NextID returns the next free VMID from the cluster.
func (c *Client) NextID(ctx context.Context) (int, error) {
	var s string
	if err := c.Get(ctx, "cluster/nextid", &s); err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

// CreateQemu creates a VM from the given config form and returns the UPID.
func (c *Client) CreateQemu(ctx context.Context, node string, form url.Values) (string, error) {
	return c.PostForm(ctx, "nodes/"+node+"/qemu", form)
}

// CloneQemu clones a VM/template to a new VMID and returns the UPID. full=true
// performs a full (independent) clone; full=false a fast linked clone.
func (c *Client) CloneQemu(ctx context.Context, node string, srcVMID, newID int, name string, full bool) (string, error) {
	form := url.Values{}
	form.Set("newid", strconv.Itoa(newID))
	if name != "" {
		form.Set("name", name)
	}
	if full {
		form.Set("full", "1")
	}
	return c.PostForm(ctx, "nodes/"+node+"/qemu/"+strconv.Itoa(srcVMID)+"/clone", form)
}

// UpdateConfig applies config changes to a VM (POST .../config). Returns the
// UPID if Proxmox runs it asynchronously, else "".
func (c *Client) UpdateConfig(ctx context.Context, node string, vmid int, form url.Values) (string, error) {
	return c.PostForm(ctx, "nodes/"+node+"/qemu/"+strconv.Itoa(vmid)+"/config", form)
}

// Snapshot creates a VM snapshot and returns the UPID.
func (c *Client) Snapshot(ctx context.Context, node string, vmid int, name, description string) (string, error) {
	form := url.Values{}
	form.Set("snapname", name)
	if description != "" {
		form.Set("description", description)
	}
	return c.PostForm(ctx, "nodes/"+node+"/qemu/"+strconv.Itoa(vmid)+"/snapshot", form)
}

// Snapshots lists a VM's snapshots.
func (c *Client) Snapshots(ctx context.Context, node string, vmid int) ([]QemuSnapshot, error) {
	var out []QemuSnapshot
	if err := c.Get(ctx, "nodes/"+node+"/qemu/"+strconv.Itoa(vmid)+"/snapshot", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// QemuSnapshot is one entry from the qemu snapshot list. The synthetic
// "current" entry (the live state, not a real snapshot) has name "current".
type QemuSnapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SnapTime    int64  `json:"snaptime"`
	Parent      string `json:"parent"`
}

// NetIface is one entry from GET /nodes/{node}/network.
type NetIface struct {
	Iface string `json:"iface"`
	Type  string `json:"type"`
}

// Bridges lists the network bridges on a node.
func (c *Client) Bridges(ctx context.Context, node string) ([]NetIface, error) {
	var out []NetIface
	if err := c.Get(ctx, "nodes/"+node+"/network?type=bridge", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Pool is one entry from GET /pools.
type Pool struct {
	PoolID  string `json:"poolid"`
	Comment string `json:"comment"`
}

// Pools lists the cluster's resource pools.
func (c *Client) Pools(ctx context.Context) ([]Pool, error) {
	var out []Pool
	if err := c.Get(ctx, "pools", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PoolAddVM adds a VM to a resource pool.
func (c *Client) PoolAddVM(ctx context.Context, poolid string, vmid int) error {
	form := url.Values{}
	form.Set("vms", strconv.Itoa(vmid))
	_, err := c.PutForm(ctx, "pools/"+url.PathEscape(poolid), form)
	return err
}

// CreatePool creates a resource pool. Pool creation is synchronous in PVE (no
// UPID task).
func (c *Client) CreatePool(ctx context.Context, poolid, comment string) error {
	form := url.Values{}
	form.Set("poolid", poolid)
	if comment != "" {
		form.Set("comment", comment)
	}
	_, err := c.PostForm(ctx, "pools", form)
	return err
}

// UpdatePoolComment sets a pool's comment. PoolIDs are immutable in PVE; only
// the comment is updatable.
func (c *Client) UpdatePoolComment(ctx context.Context, poolid, comment string) error {
	form := url.Values{}
	form.Set("comment", comment)
	_, err := c.PutForm(ctx, "pools/"+url.PathEscape(poolid), form)
	return err
}

// DeletePool deletes a resource pool. Pool deletion is synchronous in PVE (no
// UPID task).
func (c *Client) DeletePool(ctx context.Context, poolid string) error {
	_, err := c.Delete(ctx, "pools/"+url.PathEscape(poolid))
	return err
}

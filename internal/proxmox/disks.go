package proxmox

import (
	"context"
	"fmt"
)

// NodeDisk is one physical disk from GET /nodes/{node}/disks/list.
type NodeDisk struct {
	DevPath string `json:"devpath"`
	Model   string `json:"model"`
	Serial  string `json:"serial"`
	Vendor  string `json:"vendor"`
	Type    string `json:"type"` // "nvme", "ssd", "hdd"
	Size    int64  `json:"size"`
	Health  string `json:"health"`
	WWN     string `json:"wwn"`
}

// NodeDisks lists the physical disks on a node.
func (c *Client) NodeDisks(ctx context.Context, node string) ([]NodeDisk, error) {
	var out []NodeDisk
	if err := c.Get(ctx, fmt.Sprintf("nodes/%s/disks/list", node), &out); err != nil {
		return nil, err
	}
	return out, nil
}

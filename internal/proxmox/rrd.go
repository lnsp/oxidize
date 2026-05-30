package proxmox

import (
	"context"
	"fmt"
	"net/url"
)

// RRD returns a VM's RRD time-series data for the given timeframe ("hour",
// "day", "week", "month", "year"). Each point is a map with a "time" (unix
// seconds) plus metric fields (cpu, mem, maxmem, disk, maxdisk, diskread,
// diskwrite, netin, netout). Fields may be absent for gaps in the data.
func (c *Client) RRD(ctx context.Context, node string, vmid int, timeframe string) ([]map[string]any, error) {
	path := fmt.Sprintf("nodes/%s/qemu/%d/rrddata?timeframe=%s&cf=AVERAGE",
		node, vmid, url.QueryEscape(timeframe))
	var out []map[string]any
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

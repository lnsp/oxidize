package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// AllocVolume allocates a new disk image volume on a storage and returns its
// volid. sizeGB is the size in GiB.
func (c *Client) AllocVolume(ctx context.Context, node, storage string, vmid, sizeGB int, filename string) (string, error) {
	form := url.Values{}
	form.Set("vmid", strconv.Itoa(vmid))
	form.Set("filename", filename)
	form.Set("size", strconv.Itoa(sizeGB)+"G")
	form.Set("format", "raw")
	return c.PostForm(ctx, fmt.Sprintf("nodes/%s/storage/%s/content", node, storage), form)
}

// DeleteVolume frees a storage volume by volid and returns the UPID.
func (c *Client) DeleteVolume(ctx context.Context, node, volid string) (string, error) {
	return c.Delete(ctx, fmt.Sprintf("nodes/%s/storage/%s/content/%s",
		node, storageOf(volid), url.PathEscape(volid)))
}

// DeleteSnapshot removes a VM snapshot and returns the UPID.
func (c *Client) DeleteSnapshot(ctx context.Context, node string, vmid int, name string) (string, error) {
	return c.Delete(ctx, fmt.Sprintf("nodes/%s/qemu/%d/snapshot/%s", node, vmid, url.PathEscape(name)))
}

// storageOf extracts the storage id from a volid like "local-lvm:vm-100-disk-0".
func storageOf(volid string) string {
	for i := 0; i < len(volid); i++ {
		if volid[i] == ':' {
			return volid[:i]
		}
	}
	return volid
}

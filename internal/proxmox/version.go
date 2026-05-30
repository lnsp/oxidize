package proxmox

import "context"

// VersionInfo is GET /version.
type VersionInfo struct {
	Version string `json:"version"` // e.g. "9.2.3"
	Release string `json:"release"` // e.g. "9.2"
	RepoID  string `json:"repoid"`
}

// Version returns the Proxmox VE version.
func (c *Client) Version(ctx context.Context) (*VersionInfo, error) {
	var out VersionInfo
	if err := c.Get(ctx, "version", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

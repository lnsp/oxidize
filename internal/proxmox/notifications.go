package proxmox

import (
	"context"
	"net/url"
)

// Cluster notification webhook endpoints (PVE 8.1+). These are synchronous —
// they do NOT return a UPID and need no task polling, unlike VM mutations.
// PVE masks secret VALUES on read but returns their NAMES.

// WebhookEndpoint is one entry from the cluster notification webhook endpoints
// (GET /cluster/notifications/endpoints/webhook[/{name}]).
type WebhookEndpoint struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Method  string   `json:"method"`
	Comment string   `json:"comment"`
	Disable int      `json:"disable"`
	Secret  []string `json:"secret"` // secret NAMES only (values masked by PVE)
	Header  []string `json:"header"`
}

// WebhookEndpoints lists the cluster's webhook notification endpoints.
func (c *Client) WebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	var out []WebhookEndpoint
	if err := c.Get(ctx, "cluster/notifications/endpoints/webhook", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// WebhookEndpoint fetches a single webhook notification endpoint by name.
func (c *Client) WebhookEndpoint(ctx context.Context, name string) (*WebhookEndpoint, error) {
	var out WebhookEndpoint
	if err := c.Get(ctx, "cluster/notifications/endpoints/webhook/"+name, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateWebhookEndpoint creates a webhook notification endpoint. The form
// carries name, url, method, comment, disable, and any repeated secret/header
// entries. This is synchronous (no UPID).
func (c *Client) CreateWebhookEndpoint(ctx context.Context, form url.Values) error {
	_, err := c.PostForm(ctx, "cluster/notifications/endpoints/webhook", form)
	return err
}

// UpdateWebhookEndpoint updates a webhook notification endpoint. Fields are
// removed via repeated delete=<field> entries in the form. Synchronous.
func (c *Client) UpdateWebhookEndpoint(ctx context.Context, name string, form url.Values) error {
	_, err := c.PutForm(ctx, "cluster/notifications/endpoints/webhook/"+name, form)
	return err
}

// DeleteWebhookEndpoint removes a webhook notification endpoint. Synchronous.
func (c *Client) DeleteWebhookEndpoint(ctx context.Context, name string) error {
	_, err := c.Delete(ctx, "cluster/notifications/endpoints/webhook/"+name)
	return err
}

// TestNotificationTarget triggers PVE's built-in test notification for a target
// (POST /cluster/notifications/targets/{name}/test). Used to back the Oxide
// receiver "probe" best-effort; safe (sends a test notification, nothing
// destructive). Synchronous.
func (c *Client) TestNotificationTarget(ctx context.Context, name string) error {
	_, err := c.PostForm(ctx, "cluster/notifications/targets/"+name+"/test", url.Values{})
	return err
}

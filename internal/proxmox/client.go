// Package proxmox is a small typed client for the Proxmox VE API. It handles
// token auth, the {"data": ...} response envelope, form-encoded writes, and
// mapping upstream errors. See pve-apiclient/src/PVE/APIClient/LWP.pm for the
// reference transport behavior.
package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to a single Proxmox VE host.
type Client struct {
	base  string // e.g. https://host:8006
	token string // full "PVEAPIToken=user@realm!id=secret" header value
	http  *http.Client
}

// Config configures a Client.
type Config struct {
	Host               string // base URL, e.g. https://host:8006
	Token              string // full PVEAPIToken header value
	InsecureSkipVerify bool
}

// New builds a Client.
func New(cfg Config) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify},
	}
	return &Client{
		base:  strings.TrimRight(cfg.Host, "/"),
		token: cfg.Token,
		http:  &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

// Error is an upstream Proxmox error with the HTTP status it arrived with.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("proxmox: %d: %s", e.Status, e.Msg) }

// envelope is the standard Proxmox response wrapper.
type envelope struct {
	Data   json.RawMessage            `json:"data"`
	Errors map[string]json.RawMessage `json:"errors"`
}

// do performs a request and returns the unwrapped `data` payload.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (json.RawMessage, error) {
	u := c.base + "/api2/json/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Msg: err.Error()}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var env envelope
		if json.Unmarshal(raw, &env) == nil && len(env.Errors) > 0 {
			parts := make([]string, 0, len(env.Errors))
			for k, v := range env.Errors {
				parts = append(parts, fmt.Sprintf("%s: %s", k, strings.Trim(string(v), `"`)))
			}
			msg = strings.Join(parts, "; ")
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, &Error{Status: resp.StatusCode, Msg: msg}
	}

	if len(raw) == 0 {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Msg: "invalid response: " + err.Error()}
	}
	return env.Data, nil
}

// Get fetches path and unmarshals the data payload into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	data, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// PostForm submits a form-encoded POST and returns the data payload as a string
// (Proxmox returns a UPID string for async operations).
func (c *Client) PostForm(ctx context.Context, path string, form url.Values) (string, error) {
	data, err := c.do(ctx, http.MethodPost, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", err
	}
	return dataAsString(data), nil
}

// PutForm submits a form-encoded PUT and returns the data payload as a string.
func (c *Client) PutForm(ctx context.Context, path string, form url.Values) (string, error) {
	data, err := c.do(ctx, http.MethodPut, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", err
	}
	return dataAsString(data), nil
}

// Delete issues a DELETE and returns the data payload as a string (UPID).
func (c *Client) Delete(ctx context.Context, path string) (string, error) {
	data, err := c.do(ctx, http.MethodDelete, path, nil, "")
	if err != nil {
		return "", err
	}
	return dataAsString(data), nil
}

// Base returns the configured host base URL (used to build websocket URLs).
func (c *Client) Base() string { return c.base }

// Token returns the Authorization header value (used for websocket auth).
func (c *Client) Token() string { return c.token }

// HTTPClient exposes the underlying client (shares TLS config) for websocket dialing.
func (c *Client) HTTPClient() *http.Client { return c.http }

func dataAsString(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		return s
	}
	return strings.Trim(string(data), `"`)
}

package proxmox

import (
	"context"
	"fmt"
	"strings"
)

// AgentIface is one interface reported by the qemu guest agent.
type AgentIface struct {
	Name string `json:"name"`
	MAC  string `json:"hardware-address"`
	IPs  []struct {
		Addr string `json:"ip-address"`
		Type string `json:"ip-address-type"`
	} `json:"ip-addresses"`
}

type agentNetResult struct {
	Result []AgentIface `json:"result"`
}

// AgentInterfaces returns the guest agent's view of the VM's network
// interfaces. Returns an error if the agent isn't installed/running.
func (c *Client) AgentInterfaces(ctx context.Context, node string, vmid int) ([]AgentIface, error) {
	var out agentNetResult
	path := fmt.Sprintf("nodes/%s/qemu/%d/agent/network-get-interfaces", node, vmid)
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// AgentMACtoIPv4 builds a lowercase MAC -> first non-loopback IPv4 map from the
// guest agent. Returns an empty map if the agent is unavailable.
func (c *Client) AgentMACtoIPv4(ctx context.Context, node string, vmid int) map[string]string {
	ifaces, err := c.AgentInterfaces(ctx, node, vmid)
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, i := range ifaces {
		for _, a := range i.IPs {
			if a.Type == "ipv4" && !strings.HasPrefix(a.Addr, "127.") {
				out[strings.ToLower(i.MAC)] = a.Addr
				break
			}
		}
	}
	return out
}

// AgentIPv4s returns all non-loopback IPv4 addresses reported by the guest
// agent (used to surface instance external IPs).
func (c *Client) AgentIPv4s(ctx context.Context, node string, vmid int) []string {
	ifaces, err := c.AgentInterfaces(ctx, node, vmid)
	if err != nil {
		return nil
	}
	var out []string
	for _, i := range ifaces {
		if i.Name == "lo" {
			continue
		}
		for _, a := range i.IPs {
			if a.Type == "ipv4" && !strings.HasPrefix(a.Addr, "127.") {
				out = append(out, a.Addr)
			}
		}
	}
	return out
}

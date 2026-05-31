package proxmox

import (
	"context"
	"fmt"
	"net/url"
)

// Firewall API: cluster security groups + IPsets, and per-VM firewall options
// and rules. These back oxidize's VPC firewall enforcement (a per-VPC security
// group of in/out rules, referenced from each member VM). All calls are
// synchronous — the firewall endpoints don't return a UPID/task.

// FirewallGroup is one entry from GET /cluster/firewall/groups.
type FirewallGroup struct {
	Group   string `json:"group"`
	Comment string `json:"comment"`
}

// FirewallRule is one rule from a security group or a VM's rule list.
type FirewallRule struct {
	Pos     int    `json:"pos"`
	Type    string `json:"type"`   // "in" | "out" | "group"
	Action  string `json:"action"` // "ACCEPT" | "DROP" | "REJECT" | <group name>
	Proto   string `json:"proto"`
	Dport   string `json:"dport"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Enable  int    `json:"enable"`
	Comment string `json:"comment"`
}

// IPSetEntry is one entry from GET /cluster/firewall/ipset.
type IPSetEntry struct {
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

// IPSetMember is one CIDR from GET /cluster/firewall/ipset/{name}.
type IPSetMember struct {
	CIDR    string `json:"cidr"`
	Comment string `json:"comment"`
}

// FirewallOptions is the subset of a VM's firewall options oxidize manages.
type FirewallOptions struct {
	Enable    int    `json:"enable"`
	PolicyIn  string `json:"policy_in"`
	PolicyOut string `json:"policy_out"`
}

// --- cluster security groups ---

// FirewallGroups lists the cluster security groups.
func (c *Client) FirewallGroups(ctx context.Context) ([]FirewallGroup, error) {
	var out []FirewallGroup
	if err := c.Get(ctx, "cluster/firewall/groups", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateFirewallGroup creates a security group.
func (c *Client) CreateFirewallGroup(ctx context.Context, group, comment string) error {
	form := url.Values{}
	form.Set("group", group)
	if comment != "" {
		form.Set("comment", comment)
	}
	_, err := c.PostForm(ctx, "cluster/firewall/groups", form)
	return err
}

// DeleteFirewallGroup removes a security group (must have no rules).
func (c *Client) DeleteFirewallGroup(ctx context.Context, group string) error {
	_, err := c.Delete(ctx, "cluster/firewall/groups/"+url.PathEscape(group))
	return err
}

// FirewallGroupRules lists the rules in a security group.
func (c *Client) FirewallGroupRules(ctx context.Context, group string) ([]FirewallRule, error) {
	var out []FirewallRule
	if err := c.Get(ctx, "cluster/firewall/groups/"+url.PathEscape(group), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateFirewallGroupRule appends a rule to a security group.
func (c *Client) CreateFirewallGroupRule(ctx context.Context, group string, form url.Values) error {
	_, err := c.PostForm(ctx, "cluster/firewall/groups/"+url.PathEscape(group), form)
	return err
}

// DeleteFirewallGroupRule removes the rule at pos from a security group.
func (c *Client) DeleteFirewallGroupRule(ctx context.Context, group string, pos int) error {
	_, err := c.Delete(ctx, fmt.Sprintf("cluster/firewall/groups/%s/%d", url.PathEscape(group), pos))
	return err
}

// --- cluster IPsets ---

// IPSets lists the cluster IPsets.
func (c *Client) IPSets(ctx context.Context) ([]IPSetEntry, error) {
	var out []IPSetEntry
	if err := c.Get(ctx, "cluster/firewall/ipset", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateIPSet creates an IPset.
func (c *Client) CreateIPSet(ctx context.Context, name, comment string) error {
	form := url.Values{}
	form.Set("name", name)
	if comment != "" {
		form.Set("comment", comment)
	}
	_, err := c.PostForm(ctx, "cluster/firewall/ipset", form)
	return err
}

// DeleteIPSet removes an IPset (must be empty first; see DeleteIPSetMember).
func (c *Client) DeleteIPSet(ctx context.Context, name string) error {
	_, err := c.Delete(ctx, "cluster/firewall/ipset/"+url.PathEscape(name))
	return err
}

// IPSetMembers lists the CIDRs in an IPset.
func (c *Client) IPSetMembers(ctx context.Context, name string) ([]IPSetMember, error) {
	var out []IPSetMember
	if err := c.Get(ctx, "cluster/firewall/ipset/"+url.PathEscape(name), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddIPSetMember adds a CIDR to an IPset.
func (c *Client) AddIPSetMember(ctx context.Context, name, cidr, comment string) error {
	form := url.Values{}
	form.Set("cidr", cidr)
	if comment != "" {
		form.Set("comment", comment)
	}
	_, err := c.PostForm(ctx, "cluster/firewall/ipset/"+url.PathEscape(name), form)
	return err
}

// DeleteIPSetMember removes a CIDR from an IPset.
func (c *Client) DeleteIPSetMember(ctx context.Context, name, cidr string) error {
	_, err := c.Delete(ctx, "cluster/firewall/ipset/"+url.PathEscape(name)+"/"+url.PathEscape(cidr))
	return err
}

// --- per-VM firewall ---

// VMFirewallOptions returns a VM's firewall options.
func (c *Client) VMFirewallOptions(ctx context.Context, node string, vmid int) (*FirewallOptions, error) {
	var out FirewallOptions
	path := fmt.Sprintf("nodes/%s/qemu/%d/firewall/options", node, vmid)
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetVMFirewallOptions updates a VM's firewall options.
func (c *Client) SetVMFirewallOptions(ctx context.Context, node string, vmid int, form url.Values) error {
	path := fmt.Sprintf("nodes/%s/qemu/%d/firewall/options", node, vmid)
	_, err := c.PutForm(ctx, path, form)
	return err
}

// VMFirewallRules lists a VM's firewall rules.
func (c *Client) VMFirewallRules(ctx context.Context, node string, vmid int) ([]FirewallRule, error) {
	var out []FirewallRule
	path := fmt.Sprintf("nodes/%s/qemu/%d/firewall/rules", node, vmid)
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateVMFirewallRule appends a rule to a VM's rule list.
func (c *Client) CreateVMFirewallRule(ctx context.Context, node string, vmid int, form url.Values) error {
	path := fmt.Sprintf("nodes/%s/qemu/%d/firewall/rules", node, vmid)
	_, err := c.PostForm(ctx, path, form)
	return err
}

// DeleteVMFirewallRule removes the rule at pos from a VM's rule list.
func (c *Client) DeleteVMFirewallRule(ctx context.Context, node string, vmid, pos int) error {
	path := fmt.Sprintf("nodes/%s/qemu/%d/firewall/rules/%d", node, vmid, pos)
	_, err := c.Delete(ctx, path)
	return err
}

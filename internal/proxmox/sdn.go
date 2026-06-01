package proxmox

import (
	"context"
	"net/url"
)

// SDNZone is one entry from GET /cluster/sdn/zones. A zone is a network
// virtualization domain (Simple, VLAN, VXLAN, EVPN, ...).
type SDNZone struct {
	Zone string `json:"zone"`
	Type string `json:"type"`
	IPAM string `json:"ipam"`
	DHCP string `json:"dhcp"`
}

// SDNVnet is one entry from GET /cluster/sdn/vnets. A VNet is the bridge VM
// NICs attach to; its name is the Linux bridge name on each node.
type SDNVnet struct {
	Vnet string `json:"vnet"`
	Zone string `json:"zone"`
}

// SDNSubnet is one entry from GET /cluster/sdn/vnets/{vnet}/subnets.
type SDNSubnet struct {
	Subnet  string `json:"subnet"`
	CIDR    string `json:"cidr"`
	Gateway string `json:"gateway"`
	Vnet    string `json:"vnet"`
	Zone    string `json:"zone"`
	SNAT    int    `json:"snat"`
}

// SDNZones lists the cluster's SDN zones (applied config).
func (c *Client) SDNZones(ctx context.Context) ([]SDNZone, error) {
	var out []SDNZone
	if err := c.Get(ctx, "cluster/sdn/zones", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SDNVnets lists the cluster's SDN VNets.
func (c *Client) SDNVnets(ctx context.Context) ([]SDNVnet, error) {
	var out []SDNVnet
	if err := c.Get(ctx, "cluster/sdn/vnets", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SDNSubnets lists the subnets defined on a VNet.
func (c *Client) SDNSubnets(ctx context.Context, vnet string) ([]SDNSubnet, error) {
	var out []SDNSubnet
	if err := c.Get(ctx, "cluster/sdn/vnets/"+vnet+"/subnets", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SDNCreateZone stages a new SDN zone (pending until SDNApply). The form carries
// the zone type and its options (e.g. type=simple, ipam=pve, dhcp=dnsmasq).
func (c *Client) SDNCreateZone(ctx context.Context, zone string, form url.Values) error {
	form.Set("zone", zone)
	_, err := c.PostForm(ctx, "cluster/sdn/zones", form)
	return err
}

// SDNDeleteZone removes an SDN zone (pending until SDNApply). The zone must have
// no vnets.
func (c *Client) SDNDeleteZone(ctx context.Context, zone string) error {
	_, err := c.Delete(ctx, "cluster/sdn/zones/"+zone)
	return err
}

// SDNCreateVnet stages a new VNet in a zone (pending until SDNApply).
func (c *Client) SDNCreateVnet(ctx context.Context, vnet, zone string) error {
	form := url.Values{}
	form.Set("vnet", vnet)
	form.Set("zone", zone)
	_, err := c.PostForm(ctx, "cluster/sdn/vnets", form)
	return err
}

// SDNCreateSubnet stages a subnet on a VNet (pending until SDNApply).
func (c *Client) SDNCreateSubnet(ctx context.Context, vnet string, form url.Values) error {
	_, err := c.PostForm(ctx, "cluster/sdn/vnets/"+vnet+"/subnets", form)
	return err
}

// SDNDeleteVnet removes a VNet (pending until SDNApply).
func (c *Client) SDNDeleteVnet(ctx context.Context, vnet string) error {
	_, err := c.Delete(ctx, "cluster/sdn/vnets/"+vnet)
	return err
}

// SDNApply applies the staged SDN config (a reloadnetworkall task on the node).
func (c *Client) SDNApply(ctx context.Context) error {
	_, err := c.do(ctx, "PUT", "cluster/sdn", nil, "")
	return err
}

// SDNIPAMEntry is one allocation from GET /cluster/sdn/ipams/{ipam}/status.
type SDNIPAMEntry struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	Vnet     string `json:"vnet"`
}

// SDNIPAMStatus lists the pve IPAM's IP<->MAC allocations. This works without a
// guest agent and even for stopped VMs (the allocation persists), so it's the
// reliable way to learn a VM's SDN address.
func (c *Client) SDNIPAMStatus(ctx context.Context) ([]SDNIPAMEntry, error) {
	var out []SDNIPAMEntry
	if err := c.Get(ctx, "cluster/sdn/ipams/pve/status", &out); err != nil {
		return nil, err
	}
	return out, nil
}

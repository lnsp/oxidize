package translate

import (
	"strconv"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
)

// Networking ids. The "default" VPC + subnet represent the flat L2 bridge
// (vmbr0) that VMs sit on when not on an SDN VNet; Proxmox SDN zones/vnets map
// to additional VPCs/subnets (see VPCFromZone / SubnetFromVnet).
var (
	VpcID          = UUIDv5("vpc:default")
	SubnetID       = UUIDv5("subnet:default")
	SystemRouterID = UUIDv5("router:default")
)

// VPCIDForZone / SubnetIDForVnet derive stable Oxide ids from SDN object names.
func VPCIDForZone(zone string) string    { return UUIDv5("vpc:zone:" + zone) }
func SubnetIDForVnet(vnet string) string { return UUIDv5("subnet:vnet:" + vnet) }

// NICID derives a stable network-interface UUID from a vmid and device (net0).
func NICID(vmid int, dev string) string { return UUIDv5("nic:" + strconv.Itoa(vmid) + ":" + dev) }

// DefaultVPC returns the default VPC (the flat vmbr0 LAN) for a project. VPCs
// are project-scoped in Oxide but Proxmox networks are cluster-global, so the
// same VPC is surfaced in every project with that project's id.
func DefaultVPC(projectID string) oxide.Vpc {
	return oxide.Vpc{
		ID:             VpcID,
		Name:           "default",
		Description:    "Flat bridged LAN (vmbr0)",
		DNSName:        "default",
		IPv6Prefix:     "fd00::/48",
		ProjectID:      projectID,
		SystemRouterID: SystemRouterID,
		TimeCreated:    epoch,
		TimeModified:   epoch,
	}
}

// SyntheticVPC returns the default VPC scoped to the default project. Retained
// for callers that don't carry a project context.
func SyntheticVPC() oxide.Vpc { return DefaultVPC(ProjectID) }

// SyntheticSubnet returns the default subnet (the flat LAN).
func SyntheticSubnet() oxide.VpcSubnet {
	return oxide.VpcSubnet{
		ID:           SubnetID,
		Name:         "default",
		Description:  "Flat bridged LAN (vmbr0)",
		IPv4Block:    "0.0.0.0/0",
		IPv6Block:    "::/0",
		VpcID:        VpcID,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// VPCFromZone maps a Proxmox SDN zone to an Oxide VPC, scoped to projectID.
func VPCFromZone(zone, description, projectID string) oxide.Vpc {
	if description == "" {
		description = "Proxmox SDN zone " + zone
	}
	return oxide.Vpc{
		ID:             VPCIDForZone(zone),
		Name:           SanitizeName(zone, "zone"),
		Description:    description,
		DNSName:        SanitizeName(zone, "zone"),
		IPv6Prefix:     "fd00::/48",
		ProjectID:      projectID,
		SystemRouterID: SystemRouterID,
		TimeCreated:    epoch,
		TimeModified:   epoch,
	}
}

// SubnetFromVnet maps a Proxmox SDN VNet (+ its subnet CIDR) to an Oxide subnet.
// The subnet is named after the VNet, whose name is also the Linux bridge.
func SubnetFromVnet(vnet, cidr, vpcID string) oxide.VpcSubnet {
	block := cidr
	if block == "" {
		block = "0.0.0.0/0"
	}
	return oxide.VpcSubnet{
		ID:           SubnetIDForVnet(vnet),
		Name:         SanitizeName(vnet, "vnet"),
		Description:  "Proxmox SDN VNet " + vnet + " (bridge " + vnet + ")",
		IPv4Block:    block,
		IPv6Block:    "::/0",
		VpcID:        vpcID,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// NetLocator resolves a NIC's Proxmox bridge to the Oxide (vpcID, subnetID) it
// belongs to. A nil locator (or one returning empty strings) falls back to the
// default VPC/subnet (the flat vmbr0 LAN).
type NetLocator func(bridge string) (vpcID, subnetID string)

// NICFromConfig maps a Proxmox netN config entry to an Oxide NetworkInterface.
// macToIP supplies the guest-agent-reported IPv4 for the NIC's MAC, if known;
// loc maps the NIC's bridge to its VPC/subnet (SDN VNets vs the flat LAN).
func NICFromConfig(vmid int, dev, val string, macToIP map[string]string, loc NetLocator) oxide.NetworkInterface {
	model, mac, _ := parseNIC(val)
	ip := macToIP[strings.ToLower(mac)]
	bridge := bridgeOf(val)
	vpcID, subnetID := VpcID, SubnetID
	if loc != nil {
		if v, s := loc(bridge); v != "" && s != "" {
			vpcID, subnetID = v, s
		}
	}
	return oxide.NetworkInterface{
		ID:           NICID(vmid, dev),
		Name:         SanitizeName(dev, "nic"),
		Description:  "Proxmox " + model + " interface (" + bridge + ")",
		InstanceID:   InstanceID(vmid),
		IpStack:      oxide.IpStack{V4: oxide.PrivateIpv4Stack{IP: ip, TransitIps: []string{}}},
		MAC:          strings.ToUpper(mac),
		Primary:      dev == "net0",
		SubnetID:     subnetID,
		VpcID:        vpcID,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// parseNIC parses "virtio=BC:24:11:..,bridge=vmbr0,firewall=1" into model+mac.
func parseNIC(val string) (model, mac string, bridge string) {
	parts := strings.Split(val, ",")
	if len(parts) > 0 {
		if k, v, ok := cut(parts[0], '='); ok {
			model, mac = k, v
		} else {
			model = parts[0]
		}
	}
	for _, p := range parts[1:] {
		if k, v, ok := cut(p, '='); ok {
			switch k {
			case "bridge":
				bridge = v
			case "macaddr":
				mac = v
			}
		}
	}
	return model, mac, bridge
}

func bridgeOf(val string) string {
	_, _, b := parseNIC(val)
	if b == "" {
		return "vmbr0"
	}
	return b
}

// nicDevices are the Proxmox VM NIC config keys.
func isNICDevice(key string) bool {
	return strings.HasPrefix(key, "net") && len(key) > 3 && key[3] >= '0' && key[3] <= '9'
}

// NICsFromConfig extracts all netN interfaces from a VM config.
func NICsFromConfig(vmid int, cfg map[string]string, macToIP map[string]string, loc NetLocator) []oxide.NetworkInterface {
	var out []oxide.NetworkInterface
	for i := 0; i < 32; i++ {
		dev := "net" + strconv.Itoa(i)
		if val, ok := cfg[dev]; ok {
			out = append(out, NICFromConfig(vmid, dev, val, macToIP, loc))
		}
	}
	return out
}

// EphemeralExternalIP builds an Oxide ephemeral ExternalIp from an address.
func EphemeralExternalIP(ip string) map[string]any {
	return map[string]any{"ip": ip, "ip_pool_id": IPPoolID, "kind": "ephemeral"}
}

// IPPoolID is the synthetic IP pool id used for surfaced instance addresses.
var IPPoolID = UUIDv5("ip-pool:default")

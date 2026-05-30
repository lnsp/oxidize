package translate

import (
	"strconv"
	"strings"

	"github.com/lennart/oxidize/internal/oxide"
)

// Synthetic networking ids. Proxmox has no VPC/subnet concept, so we present a
// single "default" VPC + subnet that all NICs belong to, mirroring a flat L2.
var (
	VpcID          = UUIDv5("vpc:default")
	SubnetID       = UUIDv5("subnet:default")
	SystemRouterID = UUIDv5("router:default")
)

// NICID derives a stable network-interface UUID from a vmid and device (net0).
func NICID(vmid int, dev string) string { return UUIDv5("nic:" + strconv.Itoa(vmid) + ":" + dev) }

// SyntheticVPC returns the single default VPC.
func SyntheticVPC() oxide.Vpc {
	return oxide.Vpc{
		ID:             VpcID,
		Name:           "default",
		Description:    "Proxmox bridged network",
		DNSName:        "default",
		IPv6Prefix:     "fd00::/48",
		ProjectID:      ProjectID,
		SystemRouterID: SystemRouterID,
		TimeCreated:    epoch,
		TimeModified:   epoch,
	}
}

// SyntheticSubnet returns the single default subnet.
func SyntheticSubnet() oxide.VpcSubnet {
	return oxide.VpcSubnet{
		ID:           SubnetID,
		Name:         "default",
		Description:  "Proxmox bridged network",
		IPv4Block:    "0.0.0.0/0",
		IPv6Block:    "::/0",
		VpcID:        VpcID,
		TimeCreated:  epoch,
		TimeModified: epoch,
	}
}

// NICFromConfig maps a Proxmox netN config entry to an Oxide NetworkInterface.
// macToIP supplies the guest-agent-reported IPv4 for the NIC's MAC, if known.
func NICFromConfig(vmid int, dev, val string, macToIP map[string]string) oxide.NetworkInterface {
	model, mac, _ := parseNIC(val)
	ip := macToIP[strings.ToLower(mac)]
	return oxide.NetworkInterface{
		ID:           NICID(vmid, dev),
		Name:         SanitizeName(dev, "nic"),
		Description:  "Proxmox " + model + " interface (" + bridgeOf(val) + ")",
		InstanceID:   InstanceID(vmid),
		IpStack:      oxide.IpStack{V4: oxide.PrivateIpv4Stack{IP: ip, TransitIps: []string{}}},
		MAC:          strings.ToUpper(mac),
		Primary:      dev == "net0",
		SubnetID:     SubnetID,
		VpcID:        VpcID,
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
func NICsFromConfig(vmid int, cfg map[string]string, macToIP map[string]string) []oxide.NetworkInterface {
	var out []oxide.NetworkInterface
	for i := 0; i < 32; i++ {
		dev := "net" + strconv.Itoa(i)
		if val, ok := cfg[dev]; ok {
			out = append(out, NICFromConfig(vmid, dev, val, macToIP))
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

// Package oxide holds hand-written Go structs mirroring the subset of the Oxide
// external API (the "Nexus" API) that the Console UI actually consumes. Shapes
// are taken from console/app/api/__generated__/Api.ts. The wire format is
// snake_case, so every field carries an explicit json tag — a single camelCase
// leak surfaces as `undefined` in the UI.
package oxide

import (
	"encoding/json"
	"time"
)

// Time is the timestamp format the API uses (RFC3339). time.Time already
// marshals to RFC3339, so this is just a readable alias.
type Time = time.Time

// CurrentUser is returned by GET /v1/me.
type CurrentUser struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	FleetViewer  bool   `json:"fleet_viewer"`
	SiloAdmin    bool   `json:"silo_admin"`
	SiloID       string `json:"silo_id"`
	SiloName     string `json:"silo_name"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// Group is returned by GET /v1/me/groups (we return an empty list).
type Group struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	SiloID       string `json:"silo_id"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// Project is returned by GET /v1/projects and friends.
type Project struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// InstanceState mirrors the Oxide InstanceState enum (run_state).
type InstanceState string

const (
	StateCreating  InstanceState = "creating"
	StateStarting  InstanceState = "starting"
	StateRunning   InstanceState = "running"
	StateStopping  InstanceState = "stopping"
	StateStopped   InstanceState = "stopped"
	StateRebooting InstanceState = "rebooting"
	StateMigrating InstanceState = "migrating"
	StateRepairing InstanceState = "repairing"
	StateFailed    InstanceState = "failed"
	StateDestroyed InstanceState = "destroyed"

	// StateBackingUp is not part of the upstream Oxide InstanceState enum. We
	// emit it for VMs Proxmox has locked for a backup so the console shows
	// "backing up" rather than the misleading "repairing". The console badge
	// renders the raw string and tolerates an unmapped state, and the
	// capability/transition helpers treat unknown states as "no actions", which
	// is the correct behavior while a backup holds the VM lock. The value uses a
	// space (not snake_case) because the console's instance badge renders the
	// raw string verbatim, so this displays as "BACKING UP".
	StateBackingUp InstanceState = "backing up"
)

// Instance is returned by GET /v1/instances and friends. Optional/nullable
// fields are pointers so they can be emitted as JSON null.
type Instance struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Description         string        `json:"description"`
	Hostname            string        `json:"hostname"`
	ProjectID           string        `json:"project_id"`
	RunState            InstanceState `json:"run_state"`
	NCPUs               int           `json:"ncpus"`
	Memory              int64         `json:"memory"`
	BootDiskID          *string       `json:"boot_disk_id"`
	CPUPlatform         *string       `json:"cpu_platform"`
	AutoRestartEnabled  bool          `json:"auto_restart_enabled"`
	AutoRestartPolicy   *string       `json:"auto_restart_policy"`
	TimeCreated         Time          `json:"time_created"`
	TimeModified        Time          `json:"time_modified"`
	TimeRunStateUpdated Time          `json:"time_run_state_updated"`
}

// DiskState is a tagged union; see MarshalJSON in disk_state.go.
type DiskState struct {
	State    string // "attached", "detached", "creating", ...
	Instance string // set only for attach/attaching/detaching/attached
}

// Disk is returned by GET /v1/disks and friends.
type Disk struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	BlockSize    int64     `json:"block_size"`
	DevicePath   string    `json:"device_path"`
	DiskType     string    `json:"disk_type"` // "distributed" | "local"
	ImageID      *string   `json:"image_id"`
	SnapshotID   *string   `json:"snapshot_id"`
	ProjectID    string    `json:"project_id"`
	ReadOnly     bool      `json:"read_only"`
	Size         int64     `json:"size"`
	State        DiskState `json:"state"`
	TimeCreated  Time      `json:"time_created"`
	TimeModified Time      `json:"time_modified"`
}

// Image is returned by GET /v1/images and friends.
type Image struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	OS           string  `json:"os"`
	Version      string  `json:"version"`
	BlockSize    int64   `json:"block_size"`
	Size         int64   `json:"size"`
	Digest       *string `json:"digest"`
	ProjectID    *string `json:"project_id"`
	TimeCreated  Time    `json:"time_created"`
	TimeModified Time    `json:"time_modified"`
}

// Snapshot is returned by GET /v1/snapshots and friends.
type Snapshot struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	DiskID       string `json:"disk_id"`
	ProjectID    string `json:"project_id"`
	Size         int64  `json:"size"`
	State        string `json:"state"` // creating | ready | faulted | destroyed
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// Silo is returned by the system silos endpoints (we synthesize one).
type Silo struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Description      string              `json:"description"`
	Discoverable     bool                `json:"discoverable"`
	IdentityMode     string              `json:"identity_mode"`
	MappedFleetRoles map[string][]string `json:"mapped_fleet_roles"`
	AdminGroupName   *string             `json:"admin_group_name"`
	TimeCreated      Time                `json:"time_created"`
	TimeModified     Time                `json:"time_modified"`
}

// Baseboard is part of Sled.
type Baseboard struct {
	Part     string `json:"part"`
	Revision int    `json:"revision"`
	Serial   string `json:"serial"`
}

// SledPolicy is the in_service policy variant (the only one we emit).
type SledPolicy struct {
	Kind            string `json:"kind"`             // "in_service"
	ProvisionPolicy string `json:"provision_policy"` // "provisionable"
}

// Sled is returned by GET /v1/system/hardware/sleds (mapped from PVE nodes).
type Sled struct {
	ID                    string     `json:"id"`
	Baseboard             Baseboard  `json:"baseboard"`
	Policy                SledPolicy `json:"policy"`
	RackID                string     `json:"rack_id"`
	State                 string     `json:"state"` // "active"
	UsableHardwareThreads int        `json:"usable_hardware_threads"`
	UsablePhysicalRAM     int64      `json:"usable_physical_ram"`
	TimeCreated           Time       `json:"time_created"`
	TimeModified          Time       `json:"time_modified"`
}

// PhysicalDiskPolicy is the in_service policy variant.
type PhysicalDiskPolicy struct {
	Kind string `json:"kind"` // "in_service"
}

// PhysicalDisk is returned by GET /v1/system/hardware/disks (mapped from a
// Proxmox node's physical disks).
type PhysicalDisk struct {
	ID           string             `json:"id"`
	FormFactor   string             `json:"form_factor"` // "m2" | "u2"
	Model        string             `json:"model"`
	Policy       PhysicalDiskPolicy `json:"policy"`
	Serial       string             `json:"serial"`
	SledID       *string            `json:"sled_id"`
	State        string             `json:"state"` // "active"
	Vendor       string             `json:"vendor"`
	TimeCreated  Time               `json:"time_created"`
	TimeModified Time               `json:"time_modified"`
}

// SledInstance is returned by GET /v1/system/hardware/sleds/{id}/instances.
type SledInstance struct {
	ID           string        `json:"id"`
	ActiveSledID string        `json:"active_sled_id"`
	Memory       int64         `json:"memory"`
	MigrationID  *string       `json:"migration_id"`
	Name         string        `json:"name"`
	NCPUs        int           `json:"ncpus"`
	ProjectName  string        `json:"project_name"`
	SiloName     string        `json:"silo_name"`
	State        InstanceState `json:"state"`
	TimeCreated  Time          `json:"time_created"`
	TimeModified Time          `json:"time_modified"`
}

// Rack is returned by GET /v1/system/hardware/racks (we synthesize one).
type Rack struct {
	ID           string `json:"id"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// VirtualResourceCounts is the cpu/memory/storage triple used in utilization.
type VirtualResourceCounts struct {
	CPUs    int64 `json:"cpus"`
	Memory  int64 `json:"memory"`
	Storage int64 `json:"storage"`
}

// Utilization is returned by GET /v1/utilization.
type Utilization struct {
	Capacity    VirtualResourceCounts `json:"capacity"`
	Provisioned VirtualResourceCounts `json:"provisioned"`
}

// SiloUtilization is returned by the system utilization endpoints.
type SiloUtilization struct {
	Allocated   VirtualResourceCounts `json:"allocated"`
	Provisioned VirtualResourceCounts `json:"provisioned"`
	SiloID      string                `json:"silo_id"`
	SiloName    string                `json:"silo_name"`
}

// Vpc is returned by the VPC endpoints (we synthesize one "default" VPC so
// network interfaces have something to belong to).
type Vpc struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	DNSName        string `json:"dns_name"`
	IPv6Prefix     string `json:"ipv6_prefix"`
	ProjectID      string `json:"project_id"`
	SystemRouterID string `json:"system_router_id"`
	TimeCreated    Time   `json:"time_created"`
	TimeModified   Time   `json:"time_modified"`
}

// VpcFirewallRuleTarget selects the instances a firewall rule applies to. It's a
// tagged union: type is one of vpc|subnet|instance|ip|ip_net and value is the
// name/IP/CIDR (a string for every variant).
type VpcFirewallRuleTarget struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// VpcFirewallRuleHostFilter narrows a rule to the "other end" of the traffic. It
// has the same shape as a target.
type VpcFirewallRuleHostFilter struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// VpcFirewallRuleProtocol is a protocol filter. type is tcp|udp|icmp|icmp6; the
// icmp variants carry an optional {icmp_type, code} value, kept as raw JSON so
// the union round-trips without modelling every nested shape.
type VpcFirewallRuleProtocol struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
}

// VpcFirewallRuleFilter reduces the scope of a rule. All three lists are
// optional (nil = no filter of that kind).
type VpcFirewallRuleFilter struct {
	Hosts     []VpcFirewallRuleHostFilter `json:"hosts,omitempty"`
	Ports     []string                    `json:"ports,omitempty"`
	Protocols []VpcFirewallRuleProtocol   `json:"protocols,omitempty"`
}

// VpcFirewallRuleUpdate is a single rule as sent by the client in the
// vpc_firewall_rules_update body. It has no id/vpc_id/timestamps — those are
// system-controlled and synthesized server-side into the VpcFirewallRule read
// shape.
type VpcFirewallRuleUpdate struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Action      string                  `json:"action"`    // allow | deny
	Direction   string                  `json:"direction"` // inbound | outbound
	Priority    int                     `json:"priority"`
	Status      string                  `json:"status"` // enabled | disabled
	Filters     VpcFirewallRuleFilter   `json:"filters"`
	Targets     []VpcFirewallRuleTarget `json:"targets"`
}

// VpcFirewallRule is the read shape returned by the firewall-rules endpoints. It
// is the update shape plus the system-controlled id/vpc_id/timestamps.
type VpcFirewallRule struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description"`
	Action       string                  `json:"action"`
	Direction    string                  `json:"direction"`
	Priority     int                     `json:"priority"`
	Status       string                  `json:"status"`
	Filters      VpcFirewallRuleFilter   `json:"filters"`
	Targets      []VpcFirewallRuleTarget `json:"targets"`
	VpcID        string                  `json:"vpc_id"`
	TimeCreated  Time                    `json:"time_created"`
	TimeModified Time                    `json:"time_modified"`
}

// VpcSubnet is returned by the subnet endpoints (we synthesize one "default").
type VpcSubnet struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	IPv4Block      string  `json:"ipv4_block"`
	IPv6Block      string  `json:"ipv6_block"`
	VpcID          string  `json:"vpc_id"`
	CustomRouterID *string `json:"custom_router_id"`
	TimeCreated    Time    `json:"time_created"`
	TimeModified   Time    `json:"time_modified"`
}

// IpPool is returned by the IP pool endpoints (we synthesize one "default").
// The console's floating-IP / ephemeral-IP pickers filter to unicast pools and
// read ip_version / is_default, so those must be populated or the pool is hidden
// ("No pools available").
type IpPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	IPVersion    string `json:"ip_version"` // "v4" | "v6"
	PoolType     string `json:"pool_type"`  // "unicast" | "multicast"
	IsDefault    bool   `json:"is_default"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// IpRange is an inclusive address range.
type IpRange struct {
	First string `json:"first"`
	Last  string `json:"last"`
}

// IpPoolRange is one range belonging to an IP pool.
type IpPoolRange struct {
	ID          string  `json:"id"`
	IPPoolID    string  `json:"ip_pool_id"`
	Range       IpRange `json:"range"`
	TimeCreated Time    `json:"time_created"`
}

// SiloIpPool is an IP pool as seen from a silo (adds is_default for that silo).
type SiloIpPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	IPVersion    string `json:"ip_version"`
	PoolType     string `json:"pool_type"`
	IsDefault    bool   `json:"is_default"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// IpPoolSiloLink links a pool to a silo.
type IpPoolSiloLink struct {
	IPPoolID  string `json:"ip_pool_id"`
	SiloID    string `json:"silo_id"`
	IsDefault bool   `json:"is_default"`
}

// IpPoolUtilization reports address usage for a pool.
type IpPoolUtilization struct {
	Capacity  int64 `json:"capacity"`
	Remaining int64 `json:"remaining"`
}

// SubnetPool is an admin-managed pool of subnet CIDRs (also serves as the
// silo-scoped SiloSubnetPool; is_default is ignored by the system list).
type SubnetPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	IPVersion    string `json:"ip_version"`
	IsDefault    bool   `json:"is_default"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

// SubnetPoolMember is one CIDR block of a subnet pool.
type SubnetPoolMember struct {
	ID              string `json:"id"`
	Subnet          string `json:"subnet"`
	MinPrefixLength int    `json:"min_prefix_length"`
	MaxPrefixLength int    `json:"max_prefix_length"`
	SubnetPoolID    string `json:"subnet_pool_id"`
	TimeCreated     Time   `json:"time_created"`
}

// SubnetPoolSiloLink links a subnet pool to a silo.
type SubnetPoolSiloLink struct {
	SubnetPoolID string `json:"subnet_pool_id"`
	SiloID       string `json:"silo_id"`
	IsDefault    bool   `json:"is_default"`
}

// SubnetPoolUtilization reports address usage for a subnet pool.
type SubnetPoolUtilization struct {
	Capacity  int64 `json:"capacity"`
	Remaining int64 `json:"remaining"`
}

// ExternalSubnet is a routable CIDR allocated from a subnet pool, attachable to
// an instance (takahe routes the block to it — non-opaque, no NAT).
type ExternalSubnet struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	InstanceID         *string `json:"instance_id"`
	ProjectID          string  `json:"project_id"`
	Subnet             string  `json:"subnet"`
	SubnetPoolID       string  `json:"subnet_pool_id"`
	SubnetPoolMemberID string  `json:"subnet_pool_member_id"`
	TimeCreated        Time    `json:"time_created"`
	TimeModified       Time    `json:"time_modified"`
}

// FloatingIp is a standalone, reassignable address. Backed by a reserved IP in
// the SDN subnet that takahe DNATs to the attached instance's private IP.
type FloatingIp struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	IP           string  `json:"ip"`
	IPPoolID     string  `json:"ip_pool_id"`
	ProjectID    string  `json:"project_id"`
	InstanceID   *string `json:"instance_id"`
	TimeCreated  Time    `json:"time_created"`
	TimeModified Time    `json:"time_modified"`
}

// AffinityGroup is a (project-scoped) instance-placement affinity or
// anti-affinity group. oxidize records these but does not enforce placement
// (Proxmox owns the scheduler); see internal/store.AffinityGroupStore.
type AffinityGroup struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ProjectID     string `json:"project_id"`
	Policy        string `json:"policy"`         // "allow" | "fail"
	FailureDomain string `json:"failure_domain"` // "sled"
	TimeCreated   Time   `json:"time_created"`
	TimeModified  Time   `json:"time_modified"`
}

// AffinityGroupMember is a member of an affinity/anti-affinity group. Only the
// "instance" variant exists; value carries the instance's id, name, and run
// state.
type AffinityGroupMember struct {
	Type  string                   `json:"type"` // always "instance"
	Value AffinityGroupMemberValue `json:"value"`
}

// AffinityGroupMemberValue is the instance payload of an AffinityGroupMember.
type AffinityGroupMemberValue struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	RunState InstanceState `json:"run_state"`
}

// PrivateIpv4Stack is the v4 portion of a NIC's IP stack.
type PrivateIpv4Stack struct {
	IP         string   `json:"ip"`
	TransitIps []string `json:"transit_ips"`
}

// IpStack renders the Oxide PrivateIpStack tagged union (v4-only here).
type IpStack struct {
	V4 PrivateIpv4Stack
}

// MarshalJSON renders the v4 stack variant.
func (s IpStack) MarshalJSON() ([]byte, error) {
	return marshalIpStack(s.V4)
}

// NetworkInterface is returned by the network-interfaces endpoints, mapped from
// a Proxmox VM's netN devices.
type NetworkInterface struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	InstanceID   string  `json:"instance_id"`
	IpStack      IpStack `json:"ip_stack"`
	MAC          string  `json:"mac"`
	Primary      bool    `json:"primary"`
	SubnetID     string  `json:"subnet_id"`
	VpcID        string  `json:"vpc_id"`
	TimeCreated  Time    `json:"time_created"`
	TimeModified Time    `json:"time_modified"`
}

// SshKey is returned by GET /v1/me/ssh-keys (we return an empty list).
type SshKey struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	PublicKey    string `json:"public_key"`
	SiloUserID   string `json:"silo_user_id"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
}

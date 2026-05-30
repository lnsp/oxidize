// Package oxide holds hand-written Go structs mirroring the subset of the Oxide
// external API (the "Nexus" API) that the Console UI actually consumes. Shapes
// are taken from console/app/api/__generated__/Api.ts. The wire format is
// snake_case, so every field carries an explicit json tag — a single camelCase
// leak surfaces as `undefined` in the UI.
package oxide

import "time"

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
type IpPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	TimeCreated  Time   `json:"time_created"`
	TimeModified Time   `json:"time_modified"`
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

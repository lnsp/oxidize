package proxmox

// ClusterResource is one entry from GET /cluster/resources. Fields are a subset
// of what Proxmox returns; unused fields are ignored.
type ClusterResource struct {
	ID       string  `json:"id"`       // e.g. "qemu/100"
	Type     string  `json:"type"`     // "qemu", "lxc", "storage", "node", "sdn"
	VMID     int     `json:"vmid"`     // for qemu/lxc
	Node     string  `json:"node"`     // node the resource lives on
	Name     string  `json:"name"`     // VM name / storage id
	Status   string  `json:"status"`   // "running", "stopped", ...
	Template int     `json:"template"` // 1 if this is a template
	MaxMem   int64   `json:"maxmem"`   // bytes
	MaxCPU   int     `json:"maxcpu"`   // vCPU count
	MaxDisk  int64   `json:"maxdisk"`  // bytes
	Uptime   int64   `json:"uptime"`   // seconds
	CPU      float64 `json:"cpu"`      // fraction 0..1
	Mem      int64   `json:"mem"`      // bytes used
	Disk     int64   `json:"disk"`     // bytes used
	Storage  string  `json:"storage"`  // for storage resources
	Lock     string  `json:"lock"`     // present when a task holds a lock
	Pool     string  `json:"pool"`     // resource pool membership, if any
}

// Node is one entry from GET /nodes.
type Node struct {
	Node    string  `json:"node"`
	Status  string  `json:"status"` // "online" / "offline"
	MaxCPU  int     `json:"maxcpu"`
	MaxMem  int64   `json:"maxmem"`
	Mem     int64   `json:"mem"`
	CPU     float64 `json:"cpu"`
	Uptime  int64   `json:"uptime"`
	Disk    int64   `json:"disk"`
	MaxDisk int64   `json:"maxdisk"`
}

// QemuStatus is GET /nodes/{node}/qemu/{vmid}/status/current.
type QemuStatus struct {
	Status    string `json:"status"`    // "running" / "stopped"
	QmpStatus string `json:"qmpstatus"` // finer-grained: "running","paused","prelaunch"
	Lock      string `json:"lock"`      // "migrate","backup","snapshot",...
	Name      string `json:"name"`
	VMID      int    `json:"vmid"`
	MaxMem    int64  `json:"maxmem"`
	CPUs      int    `json:"cpus"`
	Uptime    int64  `json:"uptime"`
}

// TaskStatus is GET /nodes/{node}/tasks/{upid}/status.
type TaskStatus struct {
	Status     string `json:"status"`     // "running" / "stopped"
	ExitStatus string `json:"exitstatus"` // "OK" or an error string when stopped
	UPID       string `json:"upid"`
	Node       string `json:"node"`
	Type       string `json:"type"`
}

// StorageContent is one entry from GET /nodes/{node}/storage/{storage}/content.
type StorageContent struct {
	VolID   string `json:"volid"`
	Content string `json:"content"` // "iso", "vztmpl", "images", ...
	Format  string `json:"format"`  // "iso", "raw", "qcow2", ...
	Size    int64  `json:"size"`    // bytes
	VMID    int    `json:"vmid"`    // owning VM for "images" content
}

// Storage is one entry from GET /nodes/{node}/storage.
type Storage struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Content string `json:"content"` // comma-separated list of supported content types
	Active  int    `json:"active"`
}

package translate

import (
	"testing"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/proxmox"
)

func TestRunState(t *testing.T) {
	cases := []struct {
		status, qmp, lock string
		want              oxide.InstanceState
	}{
		{"running", "running", "", oxide.StateRunning},
		{"stopped", "", "", oxide.StateStopped},
		{"running", "paused", "", oxide.StateStopped},
		{"running", "", "migrate", oxide.StateMigrating},
		{"running", "", "backup", oxide.StateBackingUp},
		{"running", "", "snapshot", oxide.StateRunning}, // other locks fall through to power status
		{"stopped", "", "snapshot", oxide.StateStopped},
		{"unknown", "", "", oxide.StateStopped},
	}
	for _, c := range cases {
		if got := RunState(c.status, c.qmp, c.lock); got != c.want {
			t.Errorf("RunState(%q,%q,%q)=%q want %q", c.status, c.qmp, c.lock, got, c.want)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, fallback, want string }{
		{"web-01", "vm", "web-01"},
		{"MyVM", "vm", "myvm"},
		{"123abc", "vm", "vm-123abc"},
		{"foo_bar.baz", "vm", "foo-bar-baz"},
		{"", "vm-100", "vm-100"},
		{"--weird--", "vm", "weird"},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in, c.fallback); got != c.want {
			t.Errorf("SanitizeName(%q,%q)=%q want %q", c.in, c.fallback, got, c.want)
		}
	}
	// Must always start with a letter and be a valid Oxide Name.
	if n := SanitizeName("9", "x"); n[0] < 'a' || n[0] > 'z' {
		t.Errorf("sanitized name %q does not start with a letter", n)
	}
}

func TestParseHumanBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"32G", 32 * 1024 * 1024 * 1024},
		{"512M", 512 * 1024 * 1024},
		{"1024", 1024},
		{"1T", 1024 * 1024 * 1024 * 1024},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseHumanBytes(c.in); got != c.want {
			t.Errorf("parseHumanBytes(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestUUIDv5Deterministic(t *testing.T) {
	a := InstanceID(100)
	b := InstanceID(100)
	if a != b {
		t.Fatalf("InstanceID not deterministic: %q != %q", a, b)
	}
	if InstanceID(100) == InstanceID(101) {
		t.Fatal("distinct vmids produced the same UUID")
	}
	if len(a) != 36 || a[8] != '-' || a[14] != '5' {
		t.Errorf("UUIDv5 %q is malformed or not version 5", a)
	}
}

func TestInstanceFromResource(t *testing.T) {
	r := proxmox.ClusterResource{
		Type: "qemu", VMID: 100, Node: "pve1", Name: "Web-Server",
		Status: "running", MaxMem: 2 << 30, MaxCPU: 4,
	}
	inst := InstanceFromResource(r)
	if inst.Name != "web-server" {
		t.Errorf("name=%q want web-server", inst.Name)
	}
	if inst.RunState != oxide.StateRunning {
		t.Errorf("run_state=%q want running", inst.RunState)
	}
	if inst.NCPUs != 4 || inst.Memory != 2<<30 {
		t.Errorf("ncpus=%d memory=%d", inst.NCPUs, inst.Memory)
	}
	if inst.ProjectID != ProjectID {
		t.Error("project id mismatch")
	}
}

func TestDisksFromConfig(t *testing.T) {
	cfg := map[string]string{
		"scsi0":   "local-lvm:vm-100-disk-0,size=32G",
		"ide2":    "local:iso/debian.iso,media=cdrom", // must be skipped
		"virtio0": "local-lvm:vm-100-disk-1,size=8G",
	}
	disks := DisksFromConfig(100, InstanceID(100), ProjectID, cfg)
	if len(disks) != 2 {
		t.Fatalf("got %d disks want 2 (cdrom should be skipped)", len(disks))
	}
	var total int64
	for _, d := range disks {
		total += d.Size
		if d.State.State != "attached" || d.State.Instance != InstanceID(100) {
			t.Errorf("disk %q not attached to instance", d.Name)
		}
	}
	if want := int64((32 + 8) * 1024 * 1024 * 1024); total != want {
		t.Errorf("total size=%d want %d", total, want)
	}
}

func TestImageFromContent(t *testing.T) {
	img := ImageFromContent(proxmox.StorageContent{
		VolID: "local:iso/debian-12.5.0-amd64.iso", Content: "iso", Size: 700 << 20,
	})
	if img.OS != "Debian" {
		t.Errorf("os=%q want Debian", img.OS)
	}
	if img.Size != 700<<20 {
		t.Errorf("size=%d", img.Size)
	}
	if img.ID != ImageID("local:iso/debian-12.5.0-amd64.iso") {
		t.Error("image id mismatch")
	}
}

func TestParseOxqlQuery(t *testing.T) {
	q := `get virtual_machine:vcpu_usage | filter timestamp >= @2024-01-01T00:00:00.000 && timestamp < @2024-01-01T01:00:00.000 && instance_id == "abc-123" && state == "run" | align mean_within(60s)`
	p := ParseOxqlQuery(q)
	if p.Metric != "virtual_machine:vcpu_usage" {
		t.Errorf("metric=%q", p.Metric)
	}
	if p.InstanceRef != "abc-123" {
		t.Errorf("instance=%q", p.InstanceRef)
	}
	if p.State != "run" {
		t.Errorf("state=%q", p.State)
	}
	if p.Start.IsZero() || p.End.IsZero() || !p.End.After(p.Start) {
		t.Errorf("bad time range %v..%v", p.Start, p.End)
	}
	if TimeframeFor(p.Start, p.End) != "hour" {
		t.Errorf("timeframe=%q want hour", TimeframeFor(p.Start, p.End))
	}
}

func TestMetricValue(t *testing.T) {
	pt := map[string]any{"cpu": 0.5, "netout": 1000.0, "diskread": 2048.0}
	if v, ok := MetricValue("virtual_machine:vcpu_usage", "run", pt); !ok || v != 0.5*cpuScale {
		t.Errorf("cpu run = %v ok=%v", v, ok)
	}
	if v, ok := MetricValue("virtual_machine:vcpu_usage", "idle", pt); !ok || v != 0.5*cpuScale {
		t.Errorf("cpu idle = %v", v)
	}
	if v, ok := MetricValue("instance_network_interface:bytes_sent", "", pt); !ok || v != 1000 {
		t.Errorf("netout = %v", v)
	}
	if _, ok := MetricValue("virtual_disk:reads", "", pt); ok {
		t.Error("reads should report no data (Proxmox RRD lacks IOPS)")
	}
}

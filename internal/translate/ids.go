// Package translate converts between Proxmox VE resources and the Oxide API
// shapes the Console expects.
package translate

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// namespace is a fixed UUID used as the UUIDv5 namespace for all derived ids.
// Generated once; arbitrary but stable.
var namespace = mustParseUUID("6f3a1c2e-9b4d-5e6f-8a7b-0c1d2e3f4a5b")

// UUIDv5 derives a stable RFC-4122 v5 (SHA-1) UUID from name within our fixed
// namespace. The same name always yields the same UUID, which lets us turn
// Proxmox vmids/volids into Oxide ids without persisting a mapping.
func UUIDv5(name string) string {
	h := sha1.New()
	h.Write(namespace)
	h.Write([]byte(name))
	sum := h.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x50 // version 5
	sum[8] = (sum[8] & 0x3f) | 0x80 // RFC 4122 variant
	return formatUUID(sum)
}

// Stable ids for synthesized singletons.
var (
	ProjectID = UUIDv5("project:proxmox")
	SiloID    = UUIDv5("silo:proxmox")
	UserID    = UUIDv5("user:admin")
	RackID    = UUIDv5("rack:proxmox")
)

// InstanceID derives an instance UUID from a (cluster-unique) vmid.
func InstanceID(vmid int) string { return UUIDv5(fmt.Sprintf("vm:%d", vmid)) }

// DiskID derives a disk UUID from a vmid and device name (e.g. "scsi0").
func DiskID(vmid int, dev string) string { return UUIDv5(fmt.Sprintf("disk:%d:%s", vmid, dev)) }

// ImageID derives an image UUID from a storage volid.
func ImageID(volid string) string { return UUIDv5("image:" + volid) }

// SledID derives a sled UUID from a node name.
func SledID(node string) string { return UUIDv5("sled:" + node) }

// AffinityGroupID derives an affinity/anti-affinity group UUID from its kind,
// project id, and name (the tuple that uniquely identifies a group).
func AffinityGroupID(kind, projectID, name string) string {
	return UUIDv5(fmt.Sprintf("affinity-group:%s:%s:%s", kind, projectID, name))
}

// FirewallRuleID derives a VPC firewall rule UUID from its VPC id and name (the
// pair that uniquely identifies a rule), so a rule keeps a stable id across
// edits as long as its name is unchanged.
func FirewallRuleID(vpcID, name string) string {
	return UUIDv5(fmt.Sprintf("firewall-rule:%s:%s", vpcID, name))
}

var nameSanitize = regexp.MustCompile(`[^a-z0-9-]+`)

// SanitizeName coerces an arbitrary string into a valid Oxide Name: lowercase,
// starts with a letter, only [a-z0-9-], no trailing hyphen, max 63 chars. A
// fallback prefix keeps the result non-empty and letter-initial.
func SanitizeName(s, fallback string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nameSanitize.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return fallback
	}
	if c := s[0]; c < 'a' || c > 'z' {
		s = fallback + "-" + s
	}
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func mustParseUUID(s string) []byte {
	s = strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		panic("bad namespace uuid")
	}
	return b
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

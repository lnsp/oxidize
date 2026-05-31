// Package config loads runtime configuration from flags, environment, and the
// Proxmox TOKEN file.
package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Config holds everything the server needs to run.
type Config struct {
	// Listen is the address the HTTP server binds (e.g. ":8080").
	Listen string

	// ProxmoxHost is the base URL of the Proxmox API, e.g. https://host:8006.
	ProxmoxHost string

	// ProxmoxToken is the full Authorization header value, e.g.
	// "PVEAPIToken=root@pam!oxide=<secret>".
	ProxmoxToken string

	// InsecureSkipVerify disables TLS verification (common for self-signed
	// homelab Proxmox certs).
	InsecureSkipVerify bool

	// Console login credentials. The Proxmox token stays server-side; these
	// gate access to the UI itself.
	Username string
	Password string

	// SessionSecret signs the session cookie.
	SessionSecret []byte

	// DataDir holds file-backed state with no Proxmox equivalent (SSH keys).
	DataDir string

	// DefaultSubnet is the Oxide subnet NameOrId a new instance's primary NIC
	// attaches to when the create request doesn't pick a network explicitly
	// (the console's "Default" networking option). Empty means the node's first
	// bridge (the flat vmbr0 LAN). Set to an SDN subnet (e.g. "lab0") to make
	// that network the default and leave the flat LAN opt-in.
	DefaultSubnet string

	// FloatingRange is the inclusive address range floating IPs are allocated
	// from, as "first-last" (e.g. "172.20.10.201-172.20.10.254"). It must sit
	// inside the SDN subnet but outside its DHCP range. Empty disables floating
	// IP creation.
	FloatingRange string

	// InternalToken gates the unauthenticated internal endpoints (the floating
	// IP -> instance map the takahe reconciler polls). Empty leaves them open.
	InternalToken string
}

// LoadTokenFile parses a Proxmox TOKEN file of the form:
//
//	TOKEN_ID=root@pam!oxide
//	TOKEN_SECRET=<uuid>
//
// and returns the full PVEAPIToken Authorization header value.
func LoadTokenFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var id, secret string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "TOKEN_ID":
			id = strings.TrimSpace(v)
		case "TOKEN_SECRET":
			secret = strings.TrimSpace(v)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if id == "" || secret == "" {
		return "", fmt.Errorf("token file %s missing TOKEN_ID or TOKEN_SECRET", path)
	}
	// Header form: PVEAPIToken=USER@REALM!TOKENID=SECRET
	return fmt.Sprintf("PVEAPIToken=%s=%s", id, secret), nil
}

// Env returns the env var value or a fallback.
func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RandomSecret returns a cryptographically random hex string for signing.
func RandomSecret() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	dst := make([]byte, hex.EncodedLen(len(b)))
	hex.Encode(dst, b)
	return dst
}

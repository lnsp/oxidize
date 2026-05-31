// Command oxidize serves the Oxide Console UI backed by a Proxmox VE cluster.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/lnsp/oxidize/internal/config"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/server"
	"github.com/lnsp/oxidize/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("oxidize: ")

	var (
		listen      = flag.String("listen", config.Env("OXIDIZE_LISTEN", ":8080"), "address to listen on")
		proxmoxHost = flag.String("proxmox-host", config.Env("PROXMOX_HOST", ""), "Proxmox base URL, e.g. https://host:8006")
		tokenFile   = flag.String("token-file", config.Env("PROXMOX_TOKEN_FILE", "TOKEN"), "path to the Proxmox API token file")
		insecure    = flag.Bool("insecure", config.Env("PROXMOX_INSECURE", "true") == "true", "skip TLS verification of the Proxmox host")
		dataDir     = flag.String("data-dir", config.Env("OXIDIZE_DATA_DIR", "data"), "directory for file-backed state (SSH keys)")
		fwMode      = flag.String("firewall-mode", config.Env("OXIDIZE_FIREWALL_MODE", "off"), "VPC firewall enforcement: off | dryrun | on")
		fwInterval  = flag.String("firewall-reconcile-interval", config.Env("OXIDIZE_FIREWALL_RECONCILE_INTERVAL", "30s"), "firewall reconcile interval")
	)
	flag.Parse()

	if *proxmoxHost == "" {
		log.Fatal("missing -proxmox-host (or PROXMOX_HOST)")
	}

	switch *fwMode {
	case "off", "dryrun", "on":
	default:
		log.Fatalf("invalid -firewall-mode %q (want off, dryrun, or on)", *fwMode)
	}
	fwReconcile, err := time.ParseDuration(*fwInterval)
	if err != nil {
		log.Fatalf("invalid -firewall-reconcile-interval %q: %v", *fwInterval, err)
	}

	token, err := config.LoadTokenFile(*tokenFile)
	if err != nil {
		log.Fatalf("loading token file: %v", err)
	}

	cfg := config.Config{
		Listen:             *listen,
		ProxmoxHost:        *proxmoxHost,
		ProxmoxToken:       token,
		InsecureSkipVerify: *insecure,
		Username:           config.Env("OXIDIZE_USER", "admin"),
		Password:           config.Env("OXIDIZE_PASS", "admin"),
		DataDir:            *dataDir,
		DefaultSubnet:      config.Env("OXIDIZE_DEFAULT_SUBNET", ""),
		FloatingRange:      config.Env("OXIDIZE_FLOATING_RANGE", ""),
		InternalToken:      config.Env("OXIDIZE_INTERNAL_TOKEN", ""),

		FirewallMode:              *fwMode,
		FirewallReconcileInterval: fwReconcile,
	}
	if secret := os.Getenv("OXIDIZE_SESSION_SECRET"); secret != "" {
		cfg.SessionSecret = []byte(secret)
	} else {
		cfg.SessionSecret = config.RandomSecret()
		log.Print("warning: OXIDIZE_SESSION_SECRET not set; using a random secret (sessions drop on restart)")
	}
	if cfg.Username == "admin" && cfg.Password == "admin" {
		log.Print("warning: using default admin/admin login; set OXIDIZE_USER and OXIDIZE_PASS")
	}
	if cfg.InternalToken == "" {
		log.Print("warning: OXIDIZE_INTERNAL_TOKEN not set; /internal/floating-ip-map (instance private IPs) is served unauthenticated")
	}

	pve := proxmox.New(proxmox.Config{
		Host:               cfg.ProxmoxHost,
		Token:              cfg.ProxmoxToken,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})

	srv := server.New(cfg, pve,
		store.NewSSHKeyStore(cfg.DataDir),
		store.NewFloatingIPStore(cfg.DataDir),
		store.NewIPPoolStore(cfg.DataDir),
		store.NewSubnetPoolStore(cfg.DataDir),
		store.NewExternalSubnetStore(cfg.DataDir),
		store.NewAffinityGroupStore(cfg.DataDir),
		store.NewFirewallRuleStore(cfg.DataDir))

	// The firewall reconciler enforces VPC firewall rules on SDN-backed VPCs by
	// syncing each VPC's Proxmox security group. It is a no-op when off.
	go srv.StartFirewallReconciler(context.Background())

	log.Printf("listening on %s, proxying to %s", cfg.Listen, cfg.ProxmoxHost)
	if err := http.ListenAndServe(cfg.Listen, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

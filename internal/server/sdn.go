package server

import (
	"context"
	"time"

	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/translate"
)

// sdnTopoTTL bounds how long a fetched SDN topology is reused. The topology
// changes only when subnets/vnets are created or deleted, so a few seconds of
// staleness is fine and spares the cluster a zones+vnets+subnets sweep on every
// networking request.
const sdnTopoTTL = 5 * time.Second

// sdnTopo is a snapshot of the cluster's SDN config, used to map Proxmox
// zones/vnets/subnets onto Oxide VPCs/subnets.
type sdnTopo struct {
	zones   []proxmox.SDNZone
	vnets   []proxmox.SDNVnet
	subnets map[string][]proxmox.SDNSubnet // vnet -> its subnets
}

// sdnTopology returns the cluster SDN topology, cached for sdnTopoTTL. On a
// fetch error it serves the last good snapshot if one exists (and doesn't cache
// the failure), else an empty topology so the default VPC (flat LAN) keeps
// working. The cached value is replaced wholesale under the lock and only read
// thereafter, so sharing it across requests is safe.
func (s *Server) sdnTopology(ctx context.Context) sdnTopo {
	s.sdnMu.Lock()
	if s.sdnCache != nil && time.Now().Before(s.sdnExpiry) {
		topo := *s.sdnCache
		s.sdnMu.Unlock()
		return topo
	}
	s.sdnMu.Unlock()

	topo, err := s.fetchSDNTopology(ctx)
	if err != nil {
		s.sdnMu.Lock()
		defer s.sdnMu.Unlock()
		if s.sdnCache != nil {
			return *s.sdnCache // stale-but-usable beats empty
		}
		return topo
	}

	s.sdnMu.Lock()
	s.sdnCache = &topo
	s.sdnExpiry = time.Now().Add(sdnTopoTTL)
	s.sdnMu.Unlock()
	return topo
}

// invalidateSDNTopology drops the cached topology so the next read refetches.
// Call after mutating SDN state (vnet/subnet create or delete) so the change is
// reflected immediately instead of after the TTL.
func (s *Server) invalidateSDNTopology() {
	s.sdnMu.Lock()
	s.sdnCache = nil
	s.sdnMu.Unlock()
}

// fetchSDNTopology assembles the SDN topology from the cluster. It returns an
// error if the zone/vnet listing fails (the caller decides how to degrade);
// per-vnet subnet errors are tolerated (that vnet just has no subnets).
func (s *Server) fetchSDNTopology(ctx context.Context) (sdnTopo, error) {
	topo := sdnTopo{subnets: map[string][]proxmox.SDNSubnet{}}
	zones, err := s.pve.SDNZones(ctx)
	if err != nil {
		return topo, err
	}
	topo.zones = zones
	vnets, err := s.pve.SDNVnets(ctx)
	if err != nil {
		return topo, err
	}
	topo.vnets = vnets
	for _, v := range vnets {
		if subs, err := s.pve.SDNSubnets(ctx, v.Vnet); err == nil {
			topo.subnets[v.Vnet] = subs
		}
	}
	return topo, nil
}

// zoneOf returns the SDN zone a vnet belongs to (or "").
func (t sdnTopo) zoneOf(vnet string) string {
	for _, v := range t.vnets {
		if v.Vnet == vnet {
			return v.Zone
		}
	}
	return ""
}

// vnetsInZone returns the vnets belonging to a zone.
func (t sdnTopo) vnetsInZone(zone string) []proxmox.SDNVnet {
	var out []proxmox.SDNVnet
	for _, v := range t.vnets {
		if v.Zone == zone {
			out = append(out, v)
		}
	}
	return out
}

// cidrOf returns the first subnet CIDR defined on a vnet (or "").
func (t sdnTopo) cidrOf(vnet string) string {
	if subs := t.subnets[vnet]; len(subs) > 0 {
		return subs[0].CIDR
	}
	return ""
}

// netLocator maps a NIC's bridge name to (vpcID, subnetID): a bridge matching an
// SDN vnet resolves to that vnet's zone-VPC + subnet; anything else (vmbr0, ...)
// is the default VPC/subnet.
func (t sdnTopo) netLocator() translate.NetLocator {
	return func(bridge string) (string, string) {
		if zone := t.zoneOf(bridge); zone != "" {
			return translate.VPCIDForZone(zone), translate.SubnetIDForVnet(bridge)
		}
		return translate.VpcID, translate.SubnetID
	}
}

// vnetBridge resolves an Oxide subnet NameOrId to the Proxmox bridge (vnet name)
// to attach a NIC to. Returns "" for the default subnet / flat LAN.
func (t sdnTopo) vnetBridge(subnetRef string) string {
	if subnetRef == "" || subnetRef == "default" || subnetRef == translate.SubnetID {
		return ""
	}
	for _, v := range t.vnets {
		if v.Vnet == subnetRef ||
			translate.SanitizeName(v.Vnet, "vnet") == subnetRef ||
			translate.SubnetIDForVnet(v.Vnet) == subnetRef {
			return v.Vnet // vnet name == Linux bridge name
		}
	}
	return ""
}

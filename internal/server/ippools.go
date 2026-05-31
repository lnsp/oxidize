package server

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/store"
	"github.com/lnsp/oxidize/internal/translate"
)

// IP pools are admin-managed (System -> Networking -> IP Pools) and persisted in
// the file store. Floating/ephemeral external IPs allocate from them. There is a
// single synthetic silo, so a pool's silo link collapses to its IsDefault flag.

// ensurePools seeds an initial "default" pool (from OXIDIZE_FLOATING_RANGE) the
// first time, so prior floating-IP behavior carries over. Idempotent.
func (s *Server) ensurePools() {
	seed := store.IPPool{
		ID:           translate.IPPoolID,
		Name:         "default",
		Description:  "Default external IP pool",
		IPVersion:    "v4",
		PoolType:     "unicast",
		IsDefault:    true,
		TimeCreated:  epochTime(),
		TimeModified: epochTime(),
	}
	if first, last, ok := parseIPRange(s.cfg.FloatingRange); ok {
		seed.Ranges = []store.IPRange{{
			ID:          translate.UUIDv5("ip-range:default"),
			First:       u32ToIP(first),
			Last:        u32ToIP(last),
			TimeCreated: epochTime(),
		}}
	}
	_ = s.ippools.SeedIfEmpty(seed)
}

func toOxideIPPool(p store.IPPool) oxide.IpPool {
	return oxide.IpPool{
		ID:           p.ID,
		Name:         p.Name,
		Description:  p.Description,
		IPVersion:    p.IPVersion,
		PoolType:     p.PoolType,
		IsDefault:    p.IsDefault,
		TimeCreated:  p.TimeCreated,
		TimeModified: p.TimeModified,
	}
}

func toSiloIPPool(p store.IPPool) oxide.SiloIpPool {
	return oxide.SiloIpPool{
		ID:           p.ID,
		Name:         p.Name,
		Description:  p.Description,
		IPVersion:    p.IPVersion,
		PoolType:     p.PoolType,
		IsDefault:    p.IsDefault,
		TimeCreated:  p.TimeCreated,
		TimeModified: p.TimeModified,
	}
}

// --- silo-scoped: GET /v1/ip-pools[/{pool}] (used by the IP pickers) ---

func (s *Server) handleIPPoolList(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	pools, err := s.ippools.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]oxide.SiloIpPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, toSiloIPPool(p))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleIPPoolView(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	p, ok, err := s.ippools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, toSiloIPPool(p))
}

// --- system: GET/POST /v1/system/ip-pools, GET/PUT/DELETE .../{pool} ---

func (s *Server) handleSystemIPPoolList(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	pools, err := s.ippools.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]oxide.IpPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, toOxideIPPool(p))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

type ipPoolCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IPVersion   string `json:"ip_version"`
	PoolType    string `json:"pool_type"`
}

func (s *Server) handleSystemIPPoolCreate(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	var body ipPoolCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		oxide.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	ipVersion := body.IPVersion
	if ipVersion == "" {
		ipVersion = "v4"
	}
	poolType := body.PoolType
	if poolType == "" {
		poolType = "unicast"
	}
	now := time.Now().UTC()
	p, err := s.ippools.Create(store.IPPool{
		ID:           translate.UUIDv5("ip-pool:" + body.Name),
		Name:         body.Name,
		Description:  body.Description,
		IPVersion:    ipVersion,
		PoolType:     poolType,
		TimeCreated:  now,
		TimeModified: now,
	})
	if err != nil {
		if err == store.ErrPoolNameTaken {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, toOxideIPPool(p))
}

func (s *Server) handleSystemIPPoolView(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	p, ok, err := s.ippools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, toOxideIPPool(p))
}

type ipPoolUpdateBody struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *Server) handleSystemIPPoolUpdate(w http.ResponseWriter, r *http.Request) {
	var body ipPoolUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, ok, err := s.ippools.Update(r.PathValue("pool"), body.Name, body.Description, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, toOxideIPPool(p))
}

func (s *Server) handleSystemIPPoolDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.ippools.Delete(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- system: ranges ---

func (s *Server) handleSystemIPPoolRangeList(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	p, ok, err := s.ippools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	out := make([]oxide.IpPoolRange, 0, len(p.Ranges))
	for _, rg := range p.Ranges {
		out = append(out, oxide.IpPoolRange{
			ID:          rg.ID,
			IPPoolID:    p.ID,
			Range:       oxide.IpRange{First: rg.First, Last: rg.Last},
			TimeCreated: rg.TimeCreated,
		})
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleSystemIPPoolRangeAdd(w http.ResponseWriter, r *http.Request) {
	var rng oxide.IpRange
	if err := json.NewDecoder(r.Body).Decode(&rng); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	first, ok1 := ipToU32(net.ParseIP(rng.First))
	last, ok2 := ipToU32(net.ParseIP(rng.Last))
	if !ok1 || !ok2 || first > last {
		oxide.WriteError(w, http.StatusBadRequest, "invalid IPv4 range (first must be <= last)")
		return
	}
	now := time.Now().UTC()
	rangeID := translate.UUIDv5("ip-range:" + r.PathValue("pool") + ":" + rng.First + "-" + rng.Last)
	p, ok, err := s.ippools.AddRange(r.PathValue("pool"), rng.First, rng.Last, rangeID, now)
	if err != nil {
		if err == store.ErrRangeExists {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, oxide.IpPoolRange{
		ID: rangeID, IPPoolID: p.ID, Range: rng, TimeCreated: now,
	})
}

func (s *Server) handleSystemIPPoolRangeRemove(w http.ResponseWriter, r *http.Request) {
	var rng oxide.IpRange
	if err := json.NewDecoder(r.Body).Decode(&rng); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	_, ok, err := s.ippools.RemoveRange(r.PathValue("pool"), rng.First, rng.Last, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- system: silo links (single synthetic silo) ---

func (s *Server) handleSystemIPPoolSiloList(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.ippools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.IpPoolSiloLink{{
		IPPoolID: p.ID, SiloID: translate.SiloID, IsDefault: p.IsDefault,
	}}))
}

type ipPoolSiloLinkBody struct {
	Silo      string `json:"silo"`
	IsDefault bool   `json:"is_default"`
}

func (s *Server) handleSystemIPPoolSiloLink(w http.ResponseWriter, r *http.Request) {
	var body ipPoolSiloLinkBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	p, ok, err := s.ippools.SetDefault(r.PathValue("pool"), body.IsDefault, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, oxide.IpPoolSiloLink{
		IPPoolID: p.ID, SiloID: translate.SiloID, IsDefault: p.IsDefault,
	})
}

type ipPoolSiloUpdateBody struct {
	IsDefault bool `json:"is_default"`
}

func (s *Server) handleSystemIPPoolSiloUpdate(w http.ResponseWriter, r *http.Request) {
	var body ipPoolSiloUpdateBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	p, ok, err := s.ippools.SetDefault(r.PathValue("pool"), body.IsDefault, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.IpPoolSiloLink{
		IPPoolID: p.ID, SiloID: translate.SiloID, IsDefault: p.IsDefault,
	})
}

func (s *Server) handleSystemIPPoolSiloUnlink(w http.ResponseWriter, r *http.Request) {
	// Single synthetic silo: unlinking just clears the default flag.
	if _, _, err := s.ippools.SetDefault(r.PathValue("pool"), false, time.Now().UTC()); err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- system: utilization ---

func (s *Server) handleSystemIPPoolUtilization(w http.ResponseWriter, r *http.Request) {
	s.ensurePools()
	p, ok, err := s.ippools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ip pool not found")
		return
	}
	var capacity int64
	for _, rg := range p.Ranges {
		first, ok1 := ipToU32(net.ParseIP(rg.First))
		last, ok2 := ipToU32(net.ParseIP(rg.Last))
		if ok1 && ok2 && last >= first {
			capacity += int64(last-first) + 1
		}
	}
	allocated := int64(0)
	if used, err := s.fips.UsedIPs(); err == nil {
		for ip := range used {
			if ipInPool(p, ip) {
				allocated++
			}
		}
	}
	remaining := capacity - allocated
	if remaining < 0 {
		remaining = 0
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.IpPoolUtilization{Capacity: capacity, Remaining: remaining})
}

// --- allocation helpers (used by floating IPs) ---

// ipInPool reports whether ip falls within any of the pool's ranges.
func ipInPool(p store.IPPool, ip string) bool {
	u, ok := ipToU32(net.ParseIP(ip))
	if !ok {
		return false
	}
	for _, rg := range p.Ranges {
		first, ok1 := ipToU32(net.ParseIP(rg.First))
		last, ok2 := ipToU32(net.ParseIP(rg.Last))
		if ok1 && ok2 && u >= first && u <= last {
			return true
		}
	}
	return false
}

// defaultUnicastV4Pool returns the silo's default unicast IPv4 pool, if any.
func defaultUnicastV4Pool(pools []store.IPPool) (store.IPPool, bool) {
	for _, p := range pools {
		if p.IsDefault && p.PoolType == "unicast" && p.IPVersion == "v4" {
			return p, true
		}
	}
	return store.IPPool{}, false
}

// allocateFromPool returns the first address in the pool not already used.
func allocateFromPool(p store.IPPool, used map[string]bool) string {
	for _, rg := range p.Ranges {
		first, ok1 := ipToU32(net.ParseIP(rg.First))
		last, ok2 := ipToU32(net.ParseIP(rg.Last))
		if !ok1 || !ok2 {
			continue
		}
		for u := first; u <= last; u++ {
			ip := u32ToIP(u)
			if !used[ip] {
				return ip
			}
		}
	}
	return ""
}

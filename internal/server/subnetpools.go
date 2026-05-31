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

// Subnet pools (System -> Networking -> Subnet Pools) are admin-managed pools of
// subnet CIDRs, persisted in the file store. Note: this console's VPC-subnet
// create form takes an explicit CIDR and does not draw from a subnet pool, so
// these are standalone admin/bookkeeping objects (no allocation consumption).

func toOxideSubnetPool(p store.SubnetPool) oxide.SubnetPool {
	return oxide.SubnetPool{
		ID:           p.ID,
		Name:         p.Name,
		Description:  p.Description,
		IPVersion:    p.IPVersion,
		IsDefault:    p.IsDefault,
		TimeCreated:  p.TimeCreated,
		TimeModified: p.TimeModified,
	}
}

func (s *Server) handleSiloSubnetPoolList(w http.ResponseWriter, r *http.Request) {
	pools, err := s.subnetpools.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]oxide.SubnetPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, toOxideSubnetPool(p))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleSystemSubnetPoolList(w http.ResponseWriter, r *http.Request) {
	s.handleSiloSubnetPoolList(w, r)
}

type subnetPoolCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IPVersion   string `json:"ip_version"`
}

func (s *Server) handleSystemSubnetPoolCreate(w http.ResponseWriter, r *http.Request) {
	var body subnetPoolCreateBody
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
	now := time.Now().UTC()
	p, err := s.subnetpools.Create(store.SubnetPool{
		ID:           translate.UUIDv5("subnet-pool:" + body.Name),
		Name:         body.Name,
		Description:  body.Description,
		IPVersion:    ipVersion,
		TimeCreated:  now,
		TimeModified: now,
	})
	if err != nil {
		if err == store.ErrSubnetPoolNameTaken {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, toOxideSubnetPool(p))
}

func (s *Server) handleSystemSubnetPoolView(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.subnetpools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, toOxideSubnetPool(p))
}

func (s *Server) handleSystemSubnetPoolUpdate(w http.ResponseWriter, r *http.Request) {
	var body ipPoolUpdateBody // {name?, description?}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, ok, err := s.subnetpools.Update(r.PathValue("pool"), body.Name, body.Description, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, toOxideSubnetPool(p))
}

func (s *Server) handleSystemSubnetPoolDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.subnetpools.Delete(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- members ---

func (s *Server) handleSystemSubnetPoolMemberList(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.subnetpools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	out := make([]oxide.SubnetPoolMember, 0, len(p.Members))
	for _, m := range p.Members {
		out = append(out, oxide.SubnetPoolMember{
			ID: m.ID, Subnet: m.Subnet, MinPrefixLength: m.MinPrefixLength,
			MaxPrefixLength: m.MaxPrefixLength, SubnetPoolID: p.ID, TimeCreated: m.TimeCreated,
		})
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

type subnetMemberBody struct {
	Subnet          string `json:"subnet"`
	MinPrefixLength *int   `json:"min_prefix_length"`
	MaxPrefixLength *int   `json:"max_prefix_length"`
}

func (s *Server) handleSystemSubnetPoolMemberAdd(w http.ResponseWriter, r *http.Request) {
	var body subnetMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ip, ipnet, err := net.ParseCIDR(body.Subnet)
	if err != nil || ip.To4() == nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid IPv4 CIDR subnet")
		return
	}
	ones, _ := ipnet.Mask.Size()
	minPL, maxPL := ones, 32
	if body.MinPrefixLength != nil {
		minPL = *body.MinPrefixLength
	}
	if body.MaxPrefixLength != nil {
		maxPL = *body.MaxPrefixLength
	}
	now := time.Now().UTC()
	m := store.SubnetPoolMember{
		ID:              translate.UUIDv5("subnet-member:" + r.PathValue("pool") + ":" + body.Subnet),
		Subnet:          ipnet.String(),
		MinPrefixLength: minPL,
		MaxPrefixLength: maxPL,
		TimeCreated:     now,
	}
	p, ok, err := s.subnetpools.AddMember(r.PathValue("pool"), m, now)
	if err != nil {
		if err == store.ErrMemberExists {
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, oxide.SubnetPoolMember{
		ID: m.ID, Subnet: m.Subnet, MinPrefixLength: m.MinPrefixLength,
		MaxPrefixLength: m.MaxPrefixLength, SubnetPoolID: p.ID, TimeCreated: now,
	})
}

func (s *Server) handleSystemSubnetPoolMemberRemove(w http.ResponseWriter, r *http.Request) {
	var body subnetMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	subnet := body.Subnet
	if _, ipnet, err := net.ParseCIDR(body.Subnet); err == nil {
		subnet = ipnet.String()
	}
	_, ok, err := s.subnetpools.RemoveMember(r.PathValue("pool"), subnet, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- silo links (single synthetic silo) ---

func (s *Server) handleSystemSubnetPoolSiloList(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.subnetpools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.SubnetPoolSiloLink{{
		SubnetPoolID: p.ID, SiloID: translate.SiloID, IsDefault: p.IsDefault,
	}}))
}

func (s *Server) handleSystemSubnetPoolSiloLink(w http.ResponseWriter, r *http.Request) {
	var body ipPoolSiloLinkBody // {silo, is_default}
	_ = json.NewDecoder(r.Body).Decode(&body)
	p, ok, err := s.subnetpools.SetDefault(r.PathValue("pool"), body.IsDefault, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, oxide.SubnetPoolSiloLink{
		SubnetPoolID: p.ID, SiloID: translate.SiloID, IsDefault: p.IsDefault,
	})
}

func (s *Server) handleSystemSubnetPoolSiloUpdate(w http.ResponseWriter, r *http.Request) {
	var body ipPoolSiloUpdateBody // {is_default}
	_ = json.NewDecoder(r.Body).Decode(&body)
	p, ok, err := s.subnetpools.SetDefault(r.PathValue("pool"), body.IsDefault, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.SubnetPoolSiloLink{
		SubnetPoolID: p.ID, SiloID: translate.SiloID, IsDefault: p.IsDefault,
	})
}

func (s *Server) handleSystemSubnetPoolSiloUnlink(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.subnetpools.SetDefault(r.PathValue("pool"), false, time.Now().UTC()); err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- utilization ---

func (s *Server) handleSystemSubnetPoolUtilization(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.subnetpools.Get(r.PathValue("pool"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "subnet pool not found")
		return
	}
	var capacity int64
	for _, m := range p.Members {
		if _, ipnet, err := net.ParseCIDR(m.Subnet); err == nil {
			ones, bits := ipnet.Mask.Size()
			capacity += int64(1) << uint(bits-ones)
		}
	}
	// Nothing is allocated from subnet pools in this console, so all remains.
	oxide.WriteJSON(w, http.StatusOK, oxide.SubnetPoolUtilization{Capacity: capacity, Remaining: capacity})
}

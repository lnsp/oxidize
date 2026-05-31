package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/store"
	"github.com/lnsp/oxidize/internal/translate"
)

// External subnets are routable CIDR blocks allocated from a subnet pool and
// attached to an instance. takahe routes the block to the instance's private
// address (non-opaque; see the reconciler's route section). The operator must
// route the block to takahe and configure the guest to own the CIDR.

func (s *Server) externalSubnetToOxide(e store.ExternalSubnet) oxide.ExternalSubnet {
	var inst *string
	if e.InstanceVMID != 0 {
		id := translate.InstanceID(e.InstanceVMID)
		inst = &id
	}
	return oxide.ExternalSubnet{
		ID:                 e.ID,
		Name:               e.Name,
		Description:        e.Description,
		InstanceID:         inst,
		ProjectID:          e.ProjectID,
		Subnet:             e.Subnet,
		SubnetPoolID:       e.SubnetPoolID,
		SubnetPoolMemberID: e.SubnetPoolMemberID,
		TimeCreated:        e.TimeCreated,
		TimeModified:       e.TimeModified,
	}
}

func (s *Server) handleExternalSubnetList(w http.ResponseWriter, r *http.Request) {
	subs, err := s.extsubnets.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	projectID := s.projectIDFromRef(r.Context(), r.URL.Query().Get("project"))
	out := []oxide.ExternalSubnet{}
	for _, e := range subs {
		if e.ProjectID == projectID {
			out = append(out, s.externalSubnetToOxide(e))
		}
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

func (s *Server) handleExternalSubnetView(w http.ResponseWriter, r *http.Request) {
	e, ok, err := s.extsubnets.Get(r.PathValue("externalSubnet"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "external subnet not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.externalSubnetToOxide(e))
}

type externalSubnetCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Allocator   *struct {
		Type         string `json:"type"` // "explicit" | "auto"
		Subnet       string `json:"subnet"`
		PrefixLength int    `json:"prefix_length"`
		PoolSelector *struct {
			Type string `json:"type"`
			Pool string `json:"pool"`
		} `json:"pool_selector"`
	} `json:"allocator"`
}

func (s *Server) handleExternalSubnetCreate(w http.ResponseWriter, r *http.Request) {
	var body externalSubnetCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" || body.Allocator == nil {
		oxide.WriteError(w, http.StatusBadRequest, "name and allocator are required")
		return
	}
	pools, err := s.subnetpools.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Resolve the subnet pool: explicit pool selector, else the default v4 pool.
	var pool store.SubnetPool
	if ps := body.Allocator.PoolSelector; ps != nil && ps.Type == "explicit" && ps.Pool != "" {
		p, ok, _ := s.subnetpools.Get(ps.Pool)
		if !ok {
			oxide.WriteError(w, http.StatusBadRequest, "subnet pool not found: "+ps.Pool)
			return
		}
		pool = p
	} else if p, ok := defaultV4SubnetPool(pools); ok {
		pool = p
	} else {
		oxide.WriteError(w, http.StatusBadRequest, "no default subnet pool available")
		return
	}

	// Build the block picker (validated independently of the in-use set), then
	// let the store select + persist under its lock so concurrent allocations
	// can't pick overlapping blocks. pick must avoid overlapping any used CIDR.
	explicit := body.Allocator.Type == "explicit" && body.Allocator.Subnet != ""
	var pick func(used []string) (string, string)
	if explicit {
		_, ipnet, perr := net.ParseCIDR(body.Allocator.Subnet)
		if perr != nil {
			oxide.WriteError(w, http.StatusBadRequest, "invalid subnet CIDR")
			return
		}
		cidr := ipnet.String()
		memberID := memberContaining(pool, cidr)
		if memberID == "" {
			oxide.WriteError(w, http.StatusBadRequest, "subnet is not within any member of the pool")
			return
		}
		pick = func(used []string) (string, string) {
			if cidrOverlapsAny(cidr, used) {
				return "", ""
			}
			return cidr, memberID
		}
	} else {
		if body.Allocator.PrefixLength <= 0 || body.Allocator.PrefixLength > 32 {
			oxide.WriteError(w, http.StatusBadRequest, "prefix_length is required for auto allocation")
			return
		}
		prefixLen := body.Allocator.PrefixLength
		pick = func(used []string) (string, string) {
			return allocateSubnetFromPool(pool, prefixLen, used)
		}
	}

	now := time.Now().UTC()
	rec, err := s.extsubnets.CreateAllocated(store.ExternalSubnet{
		ID:           translate.UUIDv5("external-subnet:" + body.Name),
		Name:         body.Name,
		Description:  body.Description,
		ProjectID:    s.projectIDFromRef(r.Context(), r.URL.Query().Get("project")),
		SubnetPoolID: pool.ID,
		TimeCreated:  now,
		TimeModified: now,
	}, pick)
	if err != nil {
		switch {
		case err == store.ErrExtSubnetNameTaken:
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
		case err == store.ErrExtSubnetTaken && explicit:
			oxide.WriteError(w, http.StatusBadRequest, err.Error())
		case err == store.ErrExtSubnetTaken: // auto: no block of that size was free
			oxide.WriteError(w, http.StatusBadRequest, "no free block of that size in the subnet pool")
		default:
			oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, s.externalSubnetToOxide(rec))
}

func (s *Server) handleExternalSubnetDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.extsubnets.Delete(r.PathValue("externalSubnet"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "external subnet not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type externalSubnetAttachBody struct {
	Instance string `json:"instance"`
}

func (s *Server) handleExternalSubnetAttach(w http.ResponseWriter, r *http.Request) {
	var body externalSubnetAttachBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ref, err := s.resolveInstance(r.Context(), body.Instance)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found: "+body.Instance)
		return
	}
	e, ok, err := s.extsubnets.SetAttachment(r.PathValue("externalSubnet"), ref.vmid, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "external subnet not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.externalSubnetToOxide(e))
}

func (s *Server) handleExternalSubnetDetach(w http.ResponseWriter, r *http.Request) {
	e, ok, err := s.extsubnets.SetAttachment(r.PathValue("externalSubnet"), 0, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "external subnet not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, s.externalSubnetToOxide(e))
}

// --- subnet allocation helpers ---

func defaultV4SubnetPool(pools []store.SubnetPool) (store.SubnetPool, bool) {
	for _, p := range pools {
		if p.IsDefault && p.IPVersion == "v4" {
			return p, true
		}
	}
	return store.SubnetPool{}, false
}

// memberContaining returns the id of the pool member whose CIDR contains cidr.
func memberContaining(pool store.SubnetPool, cidr string) string {
	_, target, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	tFirst, tLast, ok := cidrBounds(target)
	if !ok {
		return ""
	}
	for _, m := range pool.Members {
		_, mnet, err := net.ParseCIDR(m.Subnet)
		if err != nil {
			continue
		}
		mFirst, mLast, ok := cidrBounds(mnet)
		if ok && tFirst >= mFirst && tLast <= mLast {
			return m.ID
		}
	}
	return ""
}

// allocateSubnetFromPool finds a free /prefixLen block within a member CIDR
// (honoring its min/max prefix) that doesn't overlap any used CIDR.
func allocateSubnetFromPool(pool store.SubnetPool, prefixLen int, used []string) (cidr, memberID string) {
	for _, m := range pool.Members {
		if prefixLen < m.MinPrefixLength || prefixLen > m.MaxPrefixLength {
			continue
		}
		_, mnet, err := net.ParseCIDR(m.Subnet)
		if err != nil {
			continue
		}
		mFirst, mLast, ok := cidrBounds(mnet)
		if !ok {
			continue
		}
		step := uint32(1) << uint(32-prefixLen)
		for addr := mFirst; addr <= mLast && addr+step-1 <= mLast; addr += step {
			cand := u32ToIP(addr) + "/" + strconv.Itoa(prefixLen)
			if !cidrOverlapsAny(cand, used) {
				return cand, m.ID
			}
			if step == 0 { // guard (prefixLen 0)
				break
			}
		}
	}
	return "", ""
}

func cidrBounds(n *net.IPNet) (first, last uint32, ok bool) {
	f, valid := ipToU32(n.IP)
	if !valid {
		return 0, 0, false
	}
	ones, bits := n.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	return f, f + size - 1, true
}

func cidrOverlapsAny(cidr string, others []string) bool {
	_, a, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	aFirst, aLast, ok := cidrBounds(a)
	if !ok {
		return false
	}
	for _, o := range others {
		_, b, err := net.ParseCIDR(o)
		if err != nil {
			continue
		}
		bFirst, bLast, ok := cidrBounds(b)
		if ok && aFirst <= bLast && bFirst <= aLast {
			return true
		}
	}
	return false
}

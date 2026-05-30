package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/translate"
)

// allImages lists ISO and container-template volumes across the cluster,
// deduplicated by volid (shared storage is visible from multiple nodes).
func (s *Server) allImages(ctx context.Context) ([]oxide.Image, error) {
	nodes, err := s.pve.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var images []oxide.Image
	for _, n := range nodes {
		// VM templates (e.g. cloud-init templates) are the primary way to
		// provision instances on Proxmox, so surface them as images too.
		if vms, err := s.pve.NodeQemu(ctx, n.Node); err == nil {
			for _, vm := range vms {
				if vm.Template == 1 {
					images = append(images, translate.ImageFromTemplate(vm))
				}
			}
		}
		storages, err := s.pve.Storages(ctx, n.Node)
		if err != nil {
			continue
		}
		for _, st := range storages {
			for _, content := range []string{"iso", "vztmpl"} {
				if !strings.Contains(st.Content, content) {
					continue
				}
				vols, err := s.pve.StorageContent(ctx, n.Node, st.Storage, content)
				if err != nil {
					continue
				}
				for _, v := range vols {
					if seen[v.VolID] {
						continue
					}
					seen[v.VolID] = true
					images = append(images, translate.ImageFromContent(v))
				}
			}
		}
	}
	return images, nil
}

func (s *Server) handleImageList(w http.ResponseWriter, r *http.Request) {
	images, err := s.allImages(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(images))
}

func (s *Server) handleImageView(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("image")
	images, err := s.allImages(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	for _, img := range images {
		if img.ID == ref || img.Name == ref {
			oxide.WriteJSON(w, http.StatusOK, img)
			return
		}
	}
	oxide.WriteError(w, http.StatusNotFound, "image not found: "+ref)
}

// handleImageDelete deletes an ISO/container-template storage volume. VM
// templates back clone-source images; deleting those would destroy the template
// VM, so we refuse and point the user at Proxmox.
func (s *Server) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref := r.PathValue("image")
	// Resolve the ref (which may be a name) to an image id by scanning.
	images, err := s.allImages(ctx)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	var imageID string
	for _, img := range images {
		if img.ID == ref || img.Name == ref {
			imageID = img.ID
			break
		}
	}
	if imageID == "" {
		oxide.WriteError(w, http.StatusNotFound, "image not found: "+ref)
		return
	}
	if tmpl := s.resolveTemplate(ctx, imageID); tmpl != nil {
		oxide.WriteError(w, http.StatusBadRequest,
			"this image is a Proxmox VM template; delete it from the VM template in Proxmox")
		return
	}
	node, volid := s.resolveImageVol(ctx, imageID)
	if volid == "" {
		oxide.WriteError(w, http.StatusNotFound, "image volume not found")
		return
	}
	upid, err := s.pve.DeleteVolume(ctx, node, volid)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	_ = s.pve.PollTask(ctx, node, upid, pveTimeout)
	w.WriteHeader(http.StatusNoContent)
}

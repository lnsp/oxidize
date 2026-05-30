package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
)

func (s *Server) handleSshKeyList(w http.ResponseWriter, r *http.Request) {
	keys, err := s.keys.List()
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(keys))
}

func (s *Server) handleSshKeyView(w http.ResponseWriter, r *http.Request) {
	key, ok, err := s.keys.Get(r.PathValue("sshKey"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ssh key not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, key)
}

type sshKeyCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PublicKey   string `json:"public_key"`
}

func (s *Server) handleSshKeyCreate(w http.ResponseWriter, r *http.Request) {
	var body sshKeyCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" || body.PublicKey == "" {
		oxide.WriteError(w, http.StatusBadRequest, "name and public_key are required")
		return
	}
	key, err := s.keys.Add(body.Name, body.Description, body.PublicKey, time.Now().UTC())
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, key)
}

func (s *Server) handleSshKeyDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.keys.Delete(r.PathValue("sshKey"))
	if err != nil {
		oxide.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		oxide.WriteError(w, http.StatusNotFound, "ssh key not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

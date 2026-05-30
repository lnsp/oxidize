package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/translate"
)

const (
	sessionCookie = "oxidize_session"
	sessionTTL    = 12 * time.Hour
)

// protected wraps a handler so it returns 401 (Oxide error body) unless a valid
// session cookie is present. The console turns any non-login 401 into a
// client-side redirect to /login, which drives the auth flow.
func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			oxide.WriteError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Constant-time compare against configured credentials.
	userOK := subtle.ConstantTimeCompare([]byte(c.Username), []byte(s.cfg.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(c.Password), []byte(s.cfg.Password)) == 1
	if !userOK || !passOK {
		// A 401 from the login POST is shown in-page by the console.
		oxide.WriteError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.setSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.CurrentUser{
		ID:           translate.UserID,
		DisplayName:  s.cfg.Username,
		FleetViewer:  true,
		SiloAdmin:    true,
		SiloID:       translate.SiloID,
		SiloName:     "proxmox",
		TimeCreated:  epochTime(),
		TimeModified: epochTime(),
	})
}

func (s *Server) handleMeGroups(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]oxide.Group{}))
}

// handleLoginRedirect sends bare /login to the synthetic silo's local login
// page, preserving redirect_uri.
func (s *Server) handleLoginRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/login/proxmox/local"
	if ru := r.URL.Query().Get("redirect_uri"); ru != "" {
		target += "?redirect_uri=" + url.QueryEscape(ru)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// --- signed cookie helpers ---

// setSession issues an HMAC-signed cookie of the form "<expiryUnix>.<sig>".
func (s *Server) setSession(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(exp, 10)
	value := payload + "." + s.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) validSession(r *http.Request) bool {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	payload, sig, ok := strings.Cut(ck.Value, ".")
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(s.sign(payload))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return true
}

func (s *Server) sign(payload string) string {
	mac := hmac.New(sha256.New, s.cfg.SessionSecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func epochTime() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) }

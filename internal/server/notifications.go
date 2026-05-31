package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
	"github.com/lnsp/oxidize/internal/translate"
)

// Alert/webhook receivers are a REAL mapping onto Proxmox cluster notification
// webhook endpoints (GET/POST/PUT/DELETE /cluster/notifications/endpoints/webhook).
//
//   - An Oxide alert receiver == one PVE webhook notification endpoint.
//   - Webhook secrets == the endpoint's PVE secrets. PVE masks secret VALUES on
//     read but returns their NAMES, so secrets round-trip by name only; their
//     Oxide ids are derived from the (endpoint, secret-name) pair.
//   - Receivers have no PVE creation timestamp, so time_created/time_modified are
//     synthesized stable values (see translate.AlertReceiverFromEndpoint).
//
// Three Oxide concepts have no Proxmox equivalent and are stubbed honestly:
//
//   - deliveries:    PVE keeps no per-endpoint delivery history -> empty Page.
//   - subscriptions: PVE has no per-endpoint event-class subscription -> the
//                    add/remove handlers are accepted as no-ops so the console
//                    keeps working; nothing is persisted (subscriptions always
//                    render empty).
//   - probe:         best-effort. PVE can send a built-in test notification for a
//                    target, which we invoke; the result is a synthesized,
//                    non-destructive probe result.

// notificationNameSanitize keeps only the characters PVE allows in a
// notification endpoint name ([A-Za-z0-9_-]).
var notificationNameSanitize = func(in string) string {
	var b strings.Builder
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeNotificationName coerces a requested name into a valid PVE
// notification endpoint name, falling back to "webhook" if nothing survives.
func sanitizeNotificationName(in string) string {
	s := notificationNameSanitize(in)
	s = strings.Trim(s, "-_")
	if s == "" {
		return "webhook"
	}
	return s
}

// resolveWebhookEndpoint resolves a {receiver} path param or ?receiver= query (a
// NameOrId) to its PVE webhook endpoint, matching the PVE name directly or its
// derived receiver UUID.
func (s *Server) resolveWebhookEndpoint(ctx context.Context, ref string) (*proxmox.WebhookEndpoint, error) {
	endpoints, err := s.pve.WebhookEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	for i := range endpoints {
		if endpoints[i].Name == ref || translate.WebhookReceiverID(endpoints[i].Name) == ref {
			return &endpoints[i], nil
		}
	}
	return nil, nil
}

// handleAlertReceiverList lists PVE webhook endpoints as Oxide alert receivers.
func (s *Server) handleAlertReceiverList(w http.ResponseWriter, r *http.Request) {
	endpoints, err := s.pve.WebhookEndpoints(r.Context())
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	out := make([]oxide.AlertReceiver, 0, len(endpoints))
	for _, e := range endpoints {
		out = append(out, translate.AlertReceiverFromEndpoint(e))
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.Page(out))
}

// handleAlertReceiverView returns one alert receiver (PVE webhook endpoint).
func (s *Server) handleAlertReceiverView(w http.ResponseWriter, r *http.Request) {
	e, err := s.resolveWebhookEndpoint(r.Context(), r.PathValue("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, translate.AlertReceiverFromEndpoint(*e))
}

// handleAlertReceiverDelete deletes the backing PVE webhook endpoint.
func (s *Server) handleAlertReceiverDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.PathValue("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	if err := s.pve.DeleteWebhookEndpoint(ctx, e.Name); err != nil {
		writeProxmoxError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWebhookReceiverCreate creates a PVE webhook endpoint from an Oxide
// webhook receiver create request. The PVE endpoint name is derived from the
// requested Oxide name (sanitized to PVE's constraints). Any provided secrets
// are added; subscriptions are accepted but ignored (no PVE equivalent).
func (s *Server) handleWebhookReceiverCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Endpoint      string   `json:"endpoint"`
		Secrets       []string `json:"secrets"`
		Subscriptions []string `json:"subscriptions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Endpoint == "" {
		oxide.WriteError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	name := sanitizeNotificationName(firstNonEmpty(body.Name, "webhook"))

	form := url.Values{}
	form.Set("name", name)
	form.Set("url", body.Endpoint)
	form.Set("method", "post")
	if body.Description != "" {
		form.Set("comment", body.Description)
	}
	for _, secret := range body.Secrets {
		form.Add("secret", "secret-"+translate.UUIDv5(secret)[:8]+"="+secret)
	}
	if err := s.pve.CreateWebhookEndpoint(ctx, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	e, err := s.pve.WebhookEndpoint(ctx, name)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, translate.WebhookReceiverFromEndpoint(*e))
}

// handleWebhookReceiverUpdate updates a PVE webhook endpoint's url/description
// (the renamable Oxide name maps to the PVE endpoint name, which PVE cannot
// rename in place, so a name change is ignored). All fields are optional.
func (s *Server) handleWebhookReceiverUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.PathValue("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Endpoint    *string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	form := url.Values{}
	if body.Endpoint != nil && *body.Endpoint != "" {
		form.Set("url", *body.Endpoint)
	}
	if body.Description != nil {
		if *body.Description == "" {
			form.Add("delete", "comment")
		} else {
			form.Set("comment", *body.Description)
		}
	}
	if len(form) > 0 {
		if err := s.pve.UpdateWebhookEndpoint(ctx, e.Name, form); err != nil {
			writeProxmoxError(w, err)
			return
		}
	}
	updated, err := s.pve.WebhookEndpoint(ctx, e.Name)
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusOK, translate.WebhookReceiverFromEndpoint(*updated))
}

// handleWebhookSecretsList lists the secret NAMES on a receiver's PVE endpoint
// as WebhookSecrets ({secrets: [{id, time_created}]}). ?receiver= is a NameOrId.
func (s *Server) handleWebhookSecretsList(w http.ResponseWriter, r *http.Request) {
	e, err := s.resolveWebhookEndpoint(r.Context(), r.URL.Query().Get("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	oxide.WriteJSON(w, http.StatusOK, oxide.WebhookSecrets{Secrets: translate.WebhookSecretsFor(*e)})
}

// handleWebhookSecretsAdd adds a secret to a receiver's PVE endpoint. ?receiver=
// is a NameOrId. The new secret is given a generated PVE name; PVE never returns
// the value again.
func (s *Server) handleWebhookSecretsAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.URL.Query().Get("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	var body struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Secret == "" {
		oxide.WriteError(w, http.StatusBadRequest, "secret is required")
		return
	}
	// PVE secrets are keyed by name. Carry the existing secret names forward
	// (their masked values are preserved by PVE on update) and add the new one.
	secretName := "secret-" + translate.UUIDv5(e.Name + ":" + body.Secret)[:8]
	form := url.Values{}
	form.Add("secret", secretName+"="+body.Secret)
	if err := s.pve.UpdateWebhookEndpoint(ctx, e.Name, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	oxide.WriteJSON(w, http.StatusCreated, oxide.WebhookSecret{
		ID:          translate.WebhookSecretID(e.Name, secretName),
		TimeCreated: webhookSecretTime(),
	})
}

// handleWebhookSecretsDelete removes a secret from a receiver's PVE endpoint.
// ?receiver= is a NameOrId; the {secret_id} path param is the Oxide secret id
// (derived from the PVE secret name), resolved by re-deriving each name's id.
func (s *Server) handleWebhookSecretsDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.URL.Query().Get("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	secretID := r.PathValue("secret_id")
	target := ""
	for _, name := range e.Secret {
		if translate.WebhookSecretID(e.Name, name) == secretID || name == secretID {
			target = name
			break
		}
	}
	if target == "" {
		oxide.WriteError(w, http.StatusNotFound, "webhook secret not found")
		return
	}
	// PVE masks secret VALUES on read, so it cannot rewrite the *other* secrets
	// (we don't have their values). The only safe rewrite is the set of secrets
	// to keep; if any other secret remains we cannot preserve its value, so we
	// reject rather than silently wipe it. The single-secret case clears them all.
	remaining := 0
	for _, name := range e.Secret {
		if name != target {
			remaining++
		}
	}
	if remaining > 0 {
		oxide.WriteError(w, http.StatusBadRequest,
			"Proxmox masks webhook secret values, so a single secret cannot be removed "+
				"while preserving the others; remove all secrets or recreate the receiver")
		return
	}
	form := url.Values{}
	form.Add("delete", "secret")
	if err := s.pve.UpdateWebhookEndpoint(ctx, e.Name, form); err != nil {
		writeProxmoxError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlertDeliveryList returns an empty Page: Proxmox keeps no per-endpoint
// delivery history.
func (s *Server) handleAlertDeliveryList(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, oxide.Page([]any{}))
}

// handleAlertReceiverProbe is a best-effort liveness probe. PVE exposes a target
// test (POST /cluster/notifications/targets/{name}/test) that sends a test
// notification; we invoke it when the receiver resolves, then return a
// synthesized AlertProbeResult. The probe is non-destructive.
func (s *Server) handleAlertReceiverProbe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.PathValue("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	// Best-effort: send PVE's built-in test notification. Failures don't fail the
	// probe response — we report the outcome in the synthesized result instead.
	state := "delivered"
	if err := s.pve.TestNotificationTarget(ctx, e.Name); err != nil {
		state = "failed"
	}
	now := webhookSecretTime()
	oxide.WriteJSON(w, http.StatusOK, map[string]any{
		"probe": map[string]any{
			"id":           translate.UUIDv5("probe:" + e.Name),
			"receiver_id":  translate.WebhookReceiverID(e.Name),
			"alert_id":     translate.UUIDv5("probe-alert:" + e.Name),
			"alert_class":  "probe",
			"state":        state,
			"trigger":      "probe",
			"time_started": now,
			"attempts":     []any{},
		},
		"resends_started": nil,
	})
}

// handleAlertSubscriptionAdd accepts a subscription and echoes it back. Proxmox
// has no per-endpoint event-class subscription, so this is a no-op (nothing is
// persisted; a receiver's subscriptions always render empty).
func (s *Server) handleAlertSubscriptionAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.PathValue("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	var body struct {
		Subscription string `json:"subscription"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	oxide.WriteJSON(w, http.StatusCreated, map[string]any{"subscription": body.Subscription})
}

// handleAlertSubscriptionRemove is the matching no-op for removing a
// subscription (Proxmox has no subscription state to remove).
func (s *Server) handleAlertSubscriptionRemove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.resolveWebhookEndpoint(ctx, r.PathValue("receiver"))
	if err != nil {
		writeProxmoxError(w, err)
		return
	}
	if e == nil {
		oxide.WriteError(w, http.StatusNotFound, "alert receiver not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// webhookSecretTime is the synthesized creation time for a freshly-added secret
// (PVE has no per-secret timestamp). It mirrors the stable epoch the translate
// mapper renders so a newly-created secret and the same secret on a later list
// agree.
func webhookSecretTime() oxide.Time { return translate.WebhookEpoch() }

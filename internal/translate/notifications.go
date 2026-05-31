package translate

import (
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/proxmox"
)

// Alert/webhook receivers map onto Proxmox cluster notification webhook
// endpoints. The mapping:
//
//   - receiver id        <- WebhookReceiverID(endpoint name)   (stable UUIDv5)
//   - name               <- endpoint name (already a valid Oxide-ish ident)
//   - description         <- endpoint comment
//   - endpoint url        <- endpoint url
//   - secrets             <- one WebhookSecret per PVE secret NAME (values are
//                            masked by PVE; only names round-trip)
//   - subscriptions       <- always empty: PVE has no per-endpoint event-class
//                            subscription concept
//   - time_created/modified <- synthesized stable value (webhookEpoch): PVE keeps
//                            no creation/modification timestamp for endpoints
//
// These mappers are pure (no I/O) so they can be unit-tested.

// webhookEpoch is the synthesized, stable creation/modification time for
// receivers and secrets. PVE exposes no timestamp for notification endpoints, so
// rather than emit a moving "now" (which would make the console think the
// resource keeps changing) we render a fixed, non-zero instant.
var webhookEpoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// WebhookEpoch returns the synthesized, stable timestamp used for receivers and
// their secrets (PVE exposes no creation time). Exposed so handlers that mint a
// fresh secret render the same instant the list mappers do.
func WebhookEpoch() time.Time { return webhookEpoch }

// WebhookSecretsFor maps a PVE endpoint's secret names to Oxide WebhookSecrets.
func WebhookSecretsFor(e proxmox.WebhookEndpoint) []oxide.WebhookSecret {
	secrets := make([]oxide.WebhookSecret, 0, len(e.Secret))
	for _, name := range e.Secret {
		secrets = append(secrets, oxide.WebhookSecret{
			ID:          WebhookSecretID(e.Name, name),
			TimeCreated: webhookEpoch,
		})
	}
	return secrets
}

// AlertReceiverFromEndpoint renders a PVE webhook endpoint as an Oxide
// AlertReceiver (kind=webhook).
func AlertReceiverFromEndpoint(e proxmox.WebhookEndpoint) oxide.AlertReceiver {
	return oxide.AlertReceiver{
		ID:          WebhookReceiverID(e.Name),
		Name:        e.Name,
		Description: e.Comment,
		Kind: oxide.AlertReceiverKind{
			Kind:     "webhook",
			Endpoint: e.URL,
			Secrets:  WebhookSecretsFor(e),
		},
		Subscriptions: []string{},
		TimeCreated:   webhookEpoch,
		TimeModified:  webhookEpoch,
	}
}

// WebhookReceiverFromEndpoint renders a PVE webhook endpoint as the flattened
// Oxide WebhookReceiver (endpoint/secrets inline).
func WebhookReceiverFromEndpoint(e proxmox.WebhookEndpoint) oxide.WebhookReceiver {
	return oxide.WebhookReceiver{
		ID:            WebhookReceiverID(e.Name),
		Name:          e.Name,
		Description:   e.Comment,
		Endpoint:      e.URL,
		Secrets:       WebhookSecretsFor(e),
		Subscriptions: []string{},
		TimeCreated:   webhookEpoch,
		TimeModified:  webhookEpoch,
	}
}

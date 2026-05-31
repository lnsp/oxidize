package translate

import (
	"testing"

	"github.com/lnsp/oxidize/internal/proxmox"
)

func TestWebhookReceiverIDStable(t *testing.T) {
	// The same PVE endpoint name always derives the same receiver UUID, and
	// distinct names derive distinct ids.
	if a, b := WebhookReceiverID("alerts"), WebhookReceiverID("alerts"); a != b {
		t.Errorf("WebhookReceiverID not stable: %q != %q", a, b)
	}
	if a, b := WebhookReceiverID("alerts"), WebhookReceiverID("other"); a == b {
		t.Errorf("WebhookReceiverID collided for distinct names: %q", a)
	}
}

func TestAlertReceiverFromEndpoint(t *testing.T) {
	e := proxmox.WebhookEndpoint{
		Name:    "alerts",
		URL:     "https://example.test/hook",
		Comment: "team alerts",
		Secret:  []string{"sig-1", "sig-2"},
	}
	r := AlertReceiverFromEndpoint(e)

	if r.ID != WebhookReceiverID("alerts") {
		t.Errorf("id = %q want %q", r.ID, WebhookReceiverID("alerts"))
	}
	if r.Name != "alerts" {
		t.Errorf("name = %q want %q", r.Name, "alerts")
	}
	if r.Description != "team alerts" {
		t.Errorf("description = %q want %q", r.Description, "team alerts")
	}
	if r.Kind.Kind != "webhook" {
		t.Errorf("kind = %q want webhook", r.Kind.Kind)
	}
	if r.Kind.Endpoint != "https://example.test/hook" {
		t.Errorf("endpoint = %q", r.Kind.Endpoint)
	}
	// One WebhookSecret per PVE secret name, with stable derived ids.
	if len(r.Kind.Secrets) != 2 {
		t.Fatalf("got %d secrets want 2", len(r.Kind.Secrets))
	}
	if r.Kind.Secrets[0].ID != WebhookSecretID("alerts", "sig-1") {
		t.Errorf("secret[0] id = %q want %q", r.Kind.Secrets[0].ID, WebhookSecretID("alerts", "sig-1"))
	}
	// PVE has no per-endpoint event-class subscription -> always empty, non-nil.
	if r.Subscriptions == nil || len(r.Subscriptions) != 0 {
		t.Errorf("subscriptions = %v want empty non-nil", r.Subscriptions)
	}
	// PVE exposes no timestamp -> the synthesized stable epoch.
	if !r.TimeCreated.Equal(WebhookEpoch()) || !r.TimeModified.Equal(WebhookEpoch()) {
		t.Errorf("timestamps = (%v,%v) want %v", r.TimeCreated, r.TimeModified, WebhookEpoch())
	}
}

func TestWebhookReceiverFromEndpoint(t *testing.T) {
	e := proxmox.WebhookEndpoint{Name: "alerts", URL: "https://x.test/h", Comment: "c", Secret: []string{"s"}}
	r := WebhookReceiverFromEndpoint(e)
	if r.ID != WebhookReceiverID("alerts") || r.Endpoint != "https://x.test/h" || r.Description != "c" {
		t.Errorf("flattened receiver mismatch: %+v", r)
	}
	if len(r.Secrets) != 1 || r.Secrets[0].ID != WebhookSecretID("alerts", "s") {
		t.Errorf("secrets mismatch: %+v", r.Secrets)
	}
	if r.Subscriptions == nil || len(r.Subscriptions) != 0 {
		t.Errorf("subscriptions = %v want empty non-nil", r.Subscriptions)
	}
}

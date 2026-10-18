// Package webhooks delivers repository events to external HTTP
// endpoints. Each webhook has a secret; payloads are signed with
// HMAC-SHA256 in the X-GitHost-Signature header so receivers can verify
// authenticity. Deliveries are recorded with their status for the UI.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"githost.dev/githost/internal/events"
	"githost.dev/githost/internal/store"
)

const (
	// SignatureHeader carries the HMAC-SHA256 of the body.
	SignatureHeader = "X-GitHost-Signature"
	// EventHeader carries the event kind.
	EventHeader = "X-GitHost-Event"
	// DeliveryTimeout bounds one webhook POST.
	DeliveryTimeout = 10 * time.Second
	// MaxPayloadBytes caps the JSON payload.
	MaxPayloadBytes = 64 << 10 // 64 KiB
)

// Deliverer sends webhooks for one repository.
type Deliverer struct {
	store  *store.Store
	client *http.Client
	logger *slog.Logger
}

// New builds a webhook deliverer.
func New(st *store.Store, logger *slog.Logger) *Deliverer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deliverer{
		store:  st,
		client: &http.Client{Timeout: DeliveryTimeout},
		logger: logger,
	}
}

// SubscribeHooks wires the deliverer into the event bus for the kinds
// a webhook can listen to.
func (d *Deliverer) SubscribeHooks(bus *events.Bus) {
	for _, kind := range []events.Kind{
		events.KindPush, events.KindIssueOpened, events.KindIssueClosed,
		events.KindPROpened, events.KindPRMerged, events.KindPRReviewed,
	} {
		bus.Subscribe(kind, func(ctx context.Context, ev events.Event) error {
			return d.DeliverForRepository(ctx, kind, ev)
		})
	}
}

// DeliverForRepository sends the event to all active webhooks of the
// repository named in the event.
func (d *Deliverer) DeliverForRepository(ctx context.Context, kind events.Kind, ev events.Event) error {
	owner, name, ok := splitRepo(ev.Repository)
	if !ok {
		return nil
	}
	repo, err := d.store.RepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		return nil // repository gone; nothing to do
	}
	hooks, err := d.store.WebhooksByRepository(ctx, repo.ID)
	if err != nil {
		return err
	}
	eventStr := string(kind)
	for _, hook := range hooks {
		if !hook.Active || !strings.Contains(hook.Events, eventStr) {
			continue
		}
		go func(hook *store.Webhook) {
			deliverCtx, cancel := context.WithTimeout(context.Background(), DeliveryTimeout)
			defer cancel()
			if err := d.deliverOne(deliverCtx, hook, eventStr, ev); err != nil {
				d.logger.Error("webhook_delivery_failed", "url", hook.URL, "err", err)
			}
		}(hook)
	}
	return nil
}

// deliverOne posts the event to one webhook and records the delivery.
func (d *Deliverer) deliverOne(ctx context.Context, hook *store.Webhook, event string, ev events.Event) error {
	payload, err := json.Marshal(map[string]any{
		"event":        event,
		"repository":   ev.Repository,
		"actor":        ev.Actor,
		"detail":       json.RawMessage(ev.Detail),
		"delivered_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if len(payload) > MaxPayloadBytes {
		return fmt.Errorf("webhook payload too large (%d bytes)", len(payload))
	}

	// Sign the payload with the webhook secret.
	mac := hmac.New(sha256.New, []byte(hook.Secret))
	mac.Write(payload)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(payload))
	if err != nil {
		return d.recordDelivery(hook.ID, event, string(payload), 0, false)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventHeader, event)
	req.Header.Set(SignatureHeader, signature)
	req.Header.Set("User-Agent", "GitHost-Webhook/1.0")

	resp, err := d.client.Do(req)
	if err != nil {
		return d.recordDelivery(hook.ID, event, string(payload), 0, false)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	return d.recordDelivery(hook.ID, event, string(payload), resp.StatusCode, success)
}

func (d *Deliverer) recordDelivery(hookID int64, event, payload string, statusCode int, success bool) error {
	// Truncate stored payloads to keep the database small.
	if len(payload) > 4096 {
		payload = payload[:4096] + "..."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.store.RecordWebhookDelivery(ctx, hookID, event, payload, statusCode, success)
}

// VerifySignature lets receivers authenticate GitHost webhooks.
func VerifySignature(secret, payload []byte, signature string) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}

func splitRepo(full string) (string, string, bool) {
	owner, name, found := strings.Cut(full, "/")
	if !found || owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}

var _ = url.QueryEscape // referenced for the net/url import

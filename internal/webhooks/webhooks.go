// Package webhooks delivers repository events to external HTTP
// endpoints. Each webhook has a secret; payloads are signed with
// HMAC-SHA256 in the X-GitHost-Signature header so receivers can verify
// authenticity. Deliveries are recorded with their status for the UI.
//
// Deliveries are best-effort: they run through a bounded pool so a busy
// repository cannot spawn unbounded goroutines, and Stop/Wait lets the
// server drain in-flight deliveries before the database closes.
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
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
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
	// MaxConcurrentDeliveries bounds the number of in-flight webhook
	// POSTs across the whole instance.
	MaxConcurrentDeliveries = 8
)

// Deliverer sends webhooks for one repository.
type Deliverer struct {
	store  *store.Store
	client *http.Client
	logger *slog.Logger
	sem    chan struct{}
	wg     sync.WaitGroup
}

// New builds a webhook deliverer. The HTTP client dials through a
// Control hook that refuses connections to loopback, unspecified,
// link-local and multicast addresses — a webhook URL must never become
// an SSRF probe into the host itself. Private LAN ranges remain
// allowed: self-hosted instances legitimately target internal CI.
func New(st *store.Store, logger *slog.Logger) *Deliverer {
	return newWithClient(st, logger, strictWebhookClient())
}

// newWithClient lets tests inject a permissive client: httptest
// receivers live on loopback, which the strict dialer refuses.
func newWithClient(st *store.Store, logger *slog.Logger, client *http.Client) *Deliverer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deliverer{
		store:  st,
		client: client,
		logger: logger,
		sem:    make(chan struct{}, MaxConcurrentDeliveries),
	}
}

// strictWebhookClient builds the production client. The Control hook is
// the dial-time authority: it sees the resolved IP, so it also defeats
// DNS rebinding between registration and delivery.
func strictWebhookClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("webhook dial: %w", err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("webhook dial: cannot parse address %q", host)
			}
			if forbiddenWebhookIP(ip) {
				return fmt.Errorf("webhook dial: address %s is a forbidden target", ip)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:   DeliveryTimeout,
		Transport: &http.Transport{DialContext: dialer.DialContext},
	}
}

// forbiddenWebhookIP reports whether an address may never be a webhook
// target. It is the dial-time authority: it sees the resolved IP, so it
// also defeats DNS rebinding between registration and delivery.
func forbiddenWebhookIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

// ValidateWebhookURL performs the cheap, registration-time half of the
// SSRF guard (scheme, userinfo, resolvable host). The dial-time Control
// check in New remains authoritative.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook url: scheme must be http or https")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("webhook url: host required")
	}
	if u.User != nil {
		return fmt.Errorf("webhook url: userinfo not allowed")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("webhook url: cannot resolve host %q", u.Hostname())
	}
	for _, ip := range ips {
		if forbiddenWebhookIP(ip) {
			return fmt.Errorf("webhook url: host %q resolves to a forbidden address (%s)", u.Hostname(), ip)
		}
	}
	return nil
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
		if err == store.ErrNotFound {
			return nil // repository gone; nothing to do
		}
		return err
	}
	hooks, err := d.store.WebhooksByRepository(ctx, repo.ID)
	if err != nil {
		return err
	}
	eventStr := string(kind)
	for _, hook := range hooks {
		if !hook.Active || !hookEventMatches(hook.Events, kind) {
			continue
		}
		select {
		case d.sem <- struct{}{}:
			d.wg.Add(1)
			go func(hook *store.Webhook) {
				defer d.wg.Done()
				defer func() { <-d.sem }()
				d.deliverLogged(hook, eventStr, ev)
			}(hook)
		default:
			// Pool saturated: deliver inline so pending work cannot grow
			// without bound. The event bus is synchronous, so this delays
			// the emitter by at most one DeliveryTimeout.
			d.deliverLogged(hook, eventStr, ev)
		}
	}
	return nil
}

// Wait blocks until all in-flight deliveries finished. Call it during
// shutdown, before the database closes.
func (d *Deliverer) Wait() {
	d.wg.Wait()
}

// hookEventMatches reports whether a hook's comma-separated event list
// subscribes to the bus kind. Hook events are coarse (push, issues,
// pulls); bus kinds are fine-grained (issue.opened, pr.merged, ...).
func hookEventMatches(events string, kind events.Kind) bool {
	k := string(kind)
	for _, e := range strings.Split(events, ",") {
		switch strings.TrimSpace(e) {
		case k:
			return true
		case "issues":
			if strings.HasPrefix(k, "issue.") {
				return true
			}
		case "pulls":
			if strings.HasPrefix(k, "pr.") {
				return true
			}
		}
	}
	return false
}

// deliverLogged runs one delivery with its own timeout and logs failures.
func (d *Deliverer) deliverLogged(hook *store.Webhook, event string, ev events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), DeliveryTimeout)
	defer cancel()
	if err := d.deliverOne(ctx, hook, event, ev); err != nil {
		d.logger.Error("webhook_delivery_failed", "url", hook.URL, "err", err)
	}
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

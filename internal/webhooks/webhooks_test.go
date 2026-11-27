package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/events"
	"githost.dev/githost/internal/security"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

type fixture struct {
	deliverer *Deliverer
	store     *store.Store
	repo      *store.Repository
	received  chan string
	server    *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()

	db, err := database.Open(ctx, base+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	hash, _ := auth.HashPassword("x")
	alice, _ := st.CreateUser(ctx, "alice", "", hash, false)
	repo, err := st.CreateRepository(ctx, alice.ID, "0123456789abcdef0123456789abcdef",
		"proj", "", store.VisibilityPrivate, "main")
	if err != nil {
		t.Fatal(err)
	}

	received := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sig := r.Header.Get(SignatureHeader); sig == "" {
			t.Error("missing signature header")
		}
		if ev := r.Header.Get(EventHeader); ev == "" {
			t.Error("missing event header")
		}
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		received <- string(body[:n])
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	return &fixture{
		deliverer: newWithClient(st, nil, &http.Client{Timeout: DeliveryTimeout}, ""),
		store:     st,
		repo:      repo,
		received:  received,
		server:    srv,
	}
}

// TestForbiddenWebhookIP pins the dial-time SSRF guard: loopback,
// unspecified, link-local, multicast and private/special targets are
// never dialable unless an operator explicitly allowlists the range.
func TestForbiddenWebhookIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "0.0.0.0", "::",
		"169.254.1.1", "fe80::1", "224.0.0.1", "ff02::1",
		"10.0.0.5", "192.168.1.10", "172.16.0.2", "100.64.0.1",
		"fc00::1", "::ffff:127.0.0.1",
	}
	for _, s := range blocked {
		if !forbiddenWebhookIP(net.ParseIP(s), nil) {
			t.Errorf("forbiddenWebhookIP(%s) = false, want true", s)
		}
	}
	// Public targets are always fine.
	for _, s := range []string{"8.8.8.8", "2001:4860:4860::8888", "1.1.1.1"} {
		if forbiddenWebhookIP(net.ParseIP(s), nil) {
			t.Errorf("forbiddenWebhookIP(%s) = true, want false", s)
		}
	}
	// An explicit allowlist opts a private range back in.
	allowed, err := parseAllowedCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	if forbiddenWebhookIP(net.ParseIP("10.0.0.5"), allowed) {
		t.Error("allowlisted private address was still forbidden")
	}
	if !forbiddenWebhookIP(net.ParseIP("192.168.1.10"), allowed) {
		t.Error("private address outside the allowlist was allowed")
	}
	// Loopback stays blocked even when an allowlist would cover it.
	loopback, err := parseAllowedCIDRs([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	if !forbiddenWebhookIP(net.ParseIP("127.0.0.1"), loopback) {
		t.Error("loopback target was allowed by CIDR allowlist")
	}
}

func TestValidateWebhookURL(t *testing.T) {
	if err := ValidateWebhookURL("https://8.8.8.8/hook"); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://10.0.0.5/hook",
		"http://169.254.169.254/latest/meta-data/",
		"https://user:pass@ci.example.test/hook",
		"ftp://ci.example.test/hook",
	} {
		if err := ValidateWebhookURL(raw); err == nil {
			t.Errorf("ValidateWebhookURL(%q) accepted a forbidden URL", raw)
		}
	}
	if err := ValidateWebhookURL("http://10.0.0.5/hook", "10.0.0.0/8"); err != nil {
		t.Fatalf("allowlisted private URL rejected: %v", err)
	}
}

func TestWebhookDelivery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	hook, err := f.store.CreateWebhook(ctx, f.repo.ID, f.server.URL, "secret", "push,issues")
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	bus := events.New(nil)
	f.deliverer.SubscribeHooks(bus)

	bus.Emit(ctx, events.Event{
		Kind:       events.KindPush,
		Repository: f.repo.Owner + "/" + f.repo.Name,
		Actor:      "alice",
		Detail:     `{"ref":"refs/heads/main"}`,
	})

	select {
	case body := <-f.received:
		if !strings.Contains(body, `"push"`) || !strings.Contains(body, f.repo.Owner+"/"+f.repo.Name) {
			t.Fatalf("payload = %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook delivery timed out")
	}

	// Delivery recording is async; poll until it appears.
	var deliveries []*store.WebhookDelivery
	deadline := time.After(3 * time.Second)
	for {
		var err error
		deliveries, err = f.store.RecentDeliveries(ctx, hook.ID, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("delivery not recorded after 3s: %v", deliveries)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !deliveries[0].Success || deliveries[0].StatusCode != 200 {
		t.Fatalf("delivery = %+v", deliveries[0])
	}
}

func TestSealedSecretDeliveryAndMandatorySignature(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	instanceSecret := "0123456789abcdef0123456789abcdef"

	sealed, err := security.Seal("a-secret-long-enough", instanceSecret)
	if err != nil {
		t.Fatal(err)
	}
	signedHook, err := f.store.CreateWebhook(ctx, f.repo.ID, f.server.URL, sealed, "push")
	if err != nil {
		t.Fatal(err)
	}
	// An unsigned hook must never be delivered.
	unsignedHook, err := f.store.CreateWebhook(ctx, f.repo.ID, f.server.URL, "", "push")
	if err != nil {
		t.Fatal(err)
	}

	d := newWithClient(f.store, nil, &http.Client{Timeout: DeliveryTimeout}, instanceSecret)
	bus := events.New(nil)
	d.SubscribeHooks(bus)
	bus.Emit(ctx, events.Event{Kind: events.KindPush, Repository: f.repo.Owner + "/" + f.repo.Name,
		Actor: "alice", Detail: `{}`})

	select {
	case body := <-f.received:
		if body == "" {
			t.Fatal("empty payload")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sealed-secret webhook delivery timed out")
	}
	// Deliveries are recorded asynchronously. Poll the database instead of
	// relying on a timing window: only the signed hook may have a record.
	deadline := time.After(3 * time.Second)
	for {
		records, err := f.store.RecentDeliveries(ctx, signedHook.ID, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("signed webhook delivery was not recorded")
		case <-time.After(50 * time.Millisecond):
		}
	}
	unsigned, err := f.store.RecentDeliveries(ctx, unsignedHook.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsigned) != 0 {
		t.Fatalf("unsigned webhook produced %d delivery records", len(unsigned))
	}
}

func TestWebhookInvalidURL(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.store.CreateWebhook(ctx, f.repo.ID, "ftp://bad", "", "push"); err == nil {
		t.Fatal("ftp:// accepted")
	}
	if _, err := f.store.CreateWebhook(ctx, f.repo.ID, "javascript:alert(1)", "", "push"); err == nil {
		t.Fatal("javascript: accepted")
	}
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("webhook-secret")
	payload := []byte(`{"event":"push"}`)
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifySignature(secret, payload, sig) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature(secret, payload, "sha256=wrong") {
		t.Fatal("wrong signature accepted")
	}
	if VerifySignature([]byte("other"), payload, sig) {
		t.Fatal("wrong secret accepted")
	}
}

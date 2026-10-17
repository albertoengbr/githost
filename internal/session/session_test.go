package session

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

func newManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	return New(st, time.Hour), st
}

func TestSessionLifecycle(t *testing.T) {
	m, st := newManager(t)
	ctx := context.Background()
	user, err := st.CreateUser(ctx, "alice", "", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	token, err := m.Start(ctx, user.ID, "127.0.0.1:1", "test-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if token == "" {
		t.Fatal("empty session token")
	}

	su, err := m.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if su == nil || su.User.Username != "alice" {
		t.Fatalf("Resolve = %+v, %v", su, err)
	}

	// Unknown tokens resolve to nil, not an error.
	su, err = m.Resolve(ctx, "not-a-real-token")
	if err != nil || su != nil {
		t.Fatalf("unknown token: %+v, %v", su, err)
	}

	if err := m.End(ctx, token); err != nil {
		t.Fatal(err)
	}
	if su, _ := m.Resolve(ctx, token); su != nil {
		t.Fatal("session still valid after End")
	}
}

func TestSessionExpiryAndPrune(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	m := New(st, time.Hour)
	ctx := context.Background()

	user, _ := st.CreateUser(ctx, "alice", "", "", false)

	// A session that expired a minute ago: not resolvable, and prunable.
	if err := st.CreateSession(ctx, store.HashToken("expired"), user.ID,
		time.Now().Add(-time.Minute), "", ""); err != nil {
		t.Fatal(err)
	}
	if su, _ := m.Resolve(ctx, "expired"); su != nil {
		t.Fatal("expired session resolved")
	}
	n, err := st.PruneSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PruneSessions = %d, %v", n, err)
	}
}

func TestCookies(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := FromRequest(r); got != "" {
		t.Fatalf("FromRequest = %q", got)
	}
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "tok"})
	if got := FromRequest(r); got != "tok" {
		t.Fatalf("FromRequest = %q", got)
	}

	w := httptest.NewRecorder()
	SetCookie(w, r, "tok", 3600)
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			found = true
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie not hardened: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("session cookie not set")
	}

	// Secure flag over TLS.
	rTLS := httptest.NewRequest(http.MethodGet, "/", nil)
	rTLS.TLS = &tls.ConnectionState{}
	w2 := httptest.NewRecorder()
	SetCookie(w2, rTLS, "tok", 3600)
	for _, c := range w2.Result().Cookies() {
		if c.Name == CookieName && !c.Secure {
			t.Fatal("Secure flag missing over TLS")
		}
	}
}

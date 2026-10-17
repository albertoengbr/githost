// Package session manages web login sessions: cookie token generation,
// persistence via the store and cookie helpers.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"githost.dev/githost/internal/store"
)

// CookieName is the session cookie.
const CookieName = "githost_session"

// DefaultTTL is how long a login lasts before it must be renewed.
const DefaultTTL = 30 * 24 * time.Hour

// Manager issues and resolves sessions.
type Manager struct {
	store *store.Store
	ttl   time.Duration
}

// New builds a session manager.
func New(st *store.Store, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Manager{store: st, ttl: ttl}
}

// Start creates a new session for the user and returns the cookie value
// to hand to the browser.
func (m *Manager) Start(ctx context.Context, userID int64, ip, userAgent string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("session token: %w", err)
	}
	token := hex.EncodeToString(buf)
	id := store.HashToken(token)
	if err := m.store.CreateSession(ctx, id, userID, time.Now().Add(m.ttl), ip, userAgent); err != nil {
		return "", err
	}
	return token, nil
}

// Resolve validates a cookie token and returns the logged-in user plus
// the session record. Expired or unknown tokens resolve to nil, nil.
func (m *Manager) Resolve(ctx context.Context, token string) (*store.SessionUser, error) {
	if token == "" {
		return nil, nil
	}
	su, err := m.store.SessionByHash(ctx, store.HashToken(token))
	if err == store.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return su, nil
}

// End deletes the session behind a cookie token (logout).
func (m *Manager) End(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return m.store.DeleteSession(ctx, store.HashToken(token))
}

// FromRequest extracts the session cookie from a request.
func FromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// SetCookie writes the session cookie. Secure is set when the request
// rides on TLS (directly; behind a TLS-terminating proxy deployments
// should terminate TLS at the proxy and keep the cookie HttpOnly +
// SameSite=Lax, documented in the runbook).
func SetCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// ClearCookie removes the session cookie.
func ClearCookie(w http.ResponseWriter, r *http.Request) {
	SetCookie(w, r, "", -1)
}

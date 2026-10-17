// Package csrf implements double-submit CSRF protection bound to a
// per-session (or per-anonymous-visitor) key.
//
// Tokens are HMAC-SHA256(instance secret, binding) values: stateless,
// constant-time verifiable, and — because the binding is the session id —
// a token stolen from one session does not work in another.
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Manager issues CSRF tokens for an instance.
type Manager struct {
	secret []byte
}

// New builds a Manager from the instance secret (the content of
// data/secret.key).
func New(secret string) *Manager {
	if secret == "" {
		// A missing secret must never silently disable CSRF: a random
		// value makes existing tokens invalid (fail closed) until the
		// instance secret is available.
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		secret = hex.EncodeToString(buf)
	}
	return &Manager{secret: []byte(secret)}
}

// Token returns the CSRF token bound to a binding key (typically the
// session id hash, or an anonymous visitor id for the login form).
func (m *Manager) Token(binding string) string {
	mac := hmac.New(sha256.New, m.secret)
	fmt.Fprintf(mac, "csrf:%s", binding)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a submitted token in constant time.
func (m *Manager) Verify(binding, token string) bool {
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(m.Token(binding))) == 1
}

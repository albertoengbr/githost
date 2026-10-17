package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"githost.dev/githost/internal/store"
)

// TokenPrefix marks GitHost access tokens ("ght_...").
const TokenPrefix = "ght_"

// Token scopes. Git operations map: fetch/clone needs repo:read,
// push needs repo:write.
const (
	ScopeRepoRead  = "repo:read"
	ScopeRepoWrite = "repo:write"
	ScopeAdmin     = "admin"
)

var validScopes = map[string]bool{
	ScopeRepoRead:  true,
	ScopeRepoWrite: true,
	ScopeAdmin:     true,
}

// GenerateToken creates a new random token. It returns the plaintext token
// (shown exactly once to the creator), its SHA-256 hex hash (stored form)
// and a short prefix for identification in listings.
func GenerateToken() (token string, tokenHash string, displayPrefix string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("auth: generate token: %w", err)
	}
	token = TokenPrefix + hex.EncodeToString(buf)
	return token, store.HashToken(token), token[:10], nil
}

// ValidateScopes checks a space-separated scope string ("" is allowed and
// yields a token that can do nothing — occasionally useful).
func ValidateScopes(scopes string) error {
	if scopes == "" {
		return nil
	}
	seen := make(map[string]bool)
	for _, scope := range strings.Fields(scopes) {
		if !validScopes[scope] {
			return fmt.Errorf("scope %q: unknown (valid: %s, %s, %s)",
				scope, ScopeRepoRead, ScopeRepoWrite, ScopeAdmin)
		}
		if seen[scope] {
			return fmt.Errorf("scope %q: duplicated", scope)
		}
		seen[scope] = true
	}
	return nil
}

// HasScope reports whether a scopes string contains the wanted scope.
func HasScope(scopes, want string) bool {
	for _, scope := range strings.Fields(scopes) {
		if scope == want {
			return true
		}
	}
	return false
}

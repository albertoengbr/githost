package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v; want true", ok, err)
	}
	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword(wrong) error: %v", err)
	}
	if ok {
		t.Fatal("wrong password must not verify")
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	bad := []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",        // wrong algorithm
		"$argon2id$v=99$m=19456,t=2,p=1$c2FsdA$aGFzaA",       // unknown version
		"$argon2id$v=19$m=99999999999,t=2,p=1$c2FsdA$aGFzaA", // absurd memory
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$aGFzaA",          // bad salt encoding
		"$argon2id$v=19$m=1024,t=0,p=1$c2FsdA$aGFzaA",        // t below range
	}
	for _, encoded := range bad {
		if _, err := VerifyPassword("x", encoded); err == nil {
			t.Errorf("VerifyPassword(%q) succeeded, want error", encoded)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePasswordPolicy("short"); err == nil {
		t.Error("short password accepted")
	}
	if err := ValidatePasswordPolicy("longenough"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
}

func TestTokenGeneration(t *testing.T) {
	token, hash, prefix, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) {
		t.Fatalf("token = %q, want %q prefix", token, TokenPrefix)
	}
	if len(token) != len(TokenPrefix)+48 {
		t.Fatalf("token length = %d", len(token))
	}
	if hash == "" || len(hash) != 64 {
		t.Fatalf("hash = %q, want 64 hex chars", hash)
	}
	if !strings.HasPrefix(token, prefix) {
		t.Fatalf("prefix %q not from token %q", prefix, token)
	}

	// A second token must differ.
	token2, _, _, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == token2 {
		t.Fatal("tokens must not repeat")
	}
}

func TestScopes(t *testing.T) {
	if err := ValidateScopes("repo:read repo:write"); err != nil {
		t.Fatalf("valid scopes rejected: %v", err)
	}
	if err := ValidateScopes(""); err != nil {
		t.Fatalf("empty scopes rejected: %v", err)
	}
	if err := ValidateScopes("repo:admin"); err == nil {
		t.Error("unknown scope accepted")
	}
	if err := ValidateScopes("repo:read repo:read"); err == nil {
		t.Error("duplicate scope accepted")
	}
	if !HasScope("repo:read repo:write", ScopeRepoWrite) {
		t.Error("HasScope(repo:write) = false")
	}
	if HasScope("repo:read", ScopeRepoWrite) {
		t.Error("HasScope missing scope = true")
	}
}

func TestParseSSHPublicKey(t *testing.T) {
	// Generate a real key pair so the fixture is always valid.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " test@example"
	canonical, fingerprint, err := ParseSSHPublicKey(line)
	if err != nil {
		t.Fatalf("ParseSSHPublicKey: %v", err)
	}
	if !strings.HasPrefix(canonical, "ssh-ed25519 ") {
		t.Fatalf("canonical = %q", canonical)
	}
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if strings.Contains(canonical, "test@example") {
		t.Fatal("canonical form must strip the comment")
	}
	// Round-trip: fingerprint of the canonical form matches.
	fp2, err := FingerprintSSHPublicKey(canonical)
	if err != nil || fp2 != fingerprint {
		t.Fatalf("fingerprint round-trip: %v %q vs %q", err, fp2, fingerprint)
	}

	for _, bad := range []string{"", "not a key", "ssh-rsa AAAA"} {
		if _, _, err := ParseSSHPublicKey(bad); err == nil {
			t.Errorf("ParseSSHPublicKey(%q) succeeded, want error", bad)
		}
	}
}

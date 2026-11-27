package security

import (
	"strings"
	"testing"
)

func TestSealRoundTripAndFreshness(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	a, err := Seal("a-long-webhook-secret", secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := Seal("a-long-webhook-secret", secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if a == b {
		t.Fatal("two seals of the same plaintext must differ (fresh nonce)")
	}
	if !strings.HasPrefix(a, SealPrefix) || strings.Contains(a, "a-long-webhook-secret") {
		t.Fatalf("sealed value leaks plaintext or lacks prefix: %q", a)
	}
	got, err := Open(a, secret)
	if err != nil || got != "a-long-webhook-secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSealRejectsWrongSecretAndPlaintext(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	sealed, err := Seal("payload", secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(sealed, "ffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("Open with wrong instance secret must fail")
	}
	if _, err := Open("plaintext", secret); err == nil {
		t.Fatal("Open of an unsealed value must fail")
	}
	if IsSealed("plaintext") || !IsSealed(sealed) {
		t.Fatal("IsSealed classification is wrong")
	}
}

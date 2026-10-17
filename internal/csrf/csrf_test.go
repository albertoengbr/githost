package csrf

import "testing"

func TestTokenBoundToKey(t *testing.T) {
	m := New("aabbccdd")

	tokSession1 := m.Token("session-1")
	tokSession2 := m.Token("session-2")
	if tokSession1 == tokSession2 {
		t.Fatal("tokens for different bindings must differ")
	}
	if !m.Verify("session-1", tokSession1) {
		t.Fatal("valid token rejected")
	}
	if m.Verify("session-2", tokSession1) {
		t.Fatal("token verified against the wrong binding")
	}
	if m.Verify("session-1", "") {
		t.Fatal("empty token accepted")
	}
	if m.Verify("session-1", "garbage") {
		t.Fatal("garbage accepted")
	}
}

func TestTokenStableAcrossManagers(t *testing.T) {
	// The same instance secret must produce the same token (restarts do
	// not invalidate outstanding forms).
	if New("secret").Token("b") != New("secret").Token("b") {
		t.Fatal("token not deterministic for the same secret")
	}
	// A different secret invalidates old forms.
	if New("secret").Token("b") == New("other").Token("b") {
		t.Fatal("different secrets must produce different tokens")
	}
}

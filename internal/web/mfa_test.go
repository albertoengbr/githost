package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	otplib "github.com/pquerna/otp/totp"

	"githost.dev/githost/internal/store"
	"githost.dev/githost/internal/totp"
)

// TestTOTPCodeCannotBeReplayed proves the MFA challenge consumes a time
// step atomically: the same valid code cannot clear a second pending
// session challenge within its ±1-step validity window.
func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user := h.createUser("dave", "correct-password-7")

	const seed = "JBSWY3DPEHPK3PXP"
	encrypted, err := totp.EncryptSecret(seed, h.secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetTOTPSecret(ctx, user.ID, encrypted); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMFAEnabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}

	const sessionToken = "pending-session-token"
	sessionHash := store.HashToken(sessionToken)
	if err := h.st.CreateSession(ctx, sessionHash, user.ID, time.Now().Add(time.Hour), "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSessionMFAPending(ctx, sessionHash, true); err != nil {
		t.Fatal(err)
	}

	code, err := otplib.GenerateCode(seed, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	verify := func() int {
		t.Helper()
		pageRec := httptest.NewRecorder()
		pageReq := httptest.NewRequest(http.MethodGet, "/mfa/challenge", nil)
		pageReq.Header.Set("Cookie", "githost_session="+sessionToken)
		h.handler.ServeHTTP(pageRec, pageReq)
		form := url.Values{}
		form.Set("csrf", csrfTokenFromHTML(t, pageRec.Body.String()))
		form.Set("code", code)
		req := httptest.NewRequest(http.MethodPost, "/mfa/verify", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Cookie", "githost_session="+sessionToken)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		return rec.Code
	}

	first := verify()
	if first != http.StatusSeeOther {
		t.Fatalf("first challenge status = %d, want 303", first)
	}
	su, err := h.st.SessionByHash(ctx, sessionHash)
	if err != nil {
		t.Fatal(err)
	}
	if su.Session.MFAPending {
		t.Fatal("first valid code did not clear the pending flag")
	}

	// Re-arm the session to observe a second verification with the same code.
	if err := h.st.SetSessionMFAPending(ctx, sessionHash, true); err != nil {
		t.Fatal(err)
	}
	second := verify()
	if second != http.StatusSeeOther {
		t.Fatalf("second challenge status = %d, want 303", second)
	}
	su, err = h.st.SessionByHash(ctx, sessionHash)
	if err != nil {
		t.Fatal(err)
	}
	if !su.Session.MFAPending {
		t.Fatal("replayed TOTP code was accepted")
	}
}

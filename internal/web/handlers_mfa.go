package web

import (
	"net/http"
	"strings"

	"githost.dev/githost/internal/totp"
)

// mfaPage renders the MFA setup/status page.
func (h *Handler) mfaPage(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	enabled, _ := h.deps.Store.IsMFAEnabled(ctx, su.User.ID)
	unused, _ := h.deps.Store.UnusedRecoveryCodes(ctx, su.User.ID)

	data := struct {
		pageData
		MFAEnabled    bool
		UnusedCodes   int
		Secret        string
		OTPAuthURL    string
		FormError     string
		RecoveryCodes []string
	}{pageData: pd, MFAEnabled: enabled, UnusedCodes: unused}

	if !enabled {
		// Generate a new secret for setup (stored only on enable).
		secret, url, err := totp.Generate(su.User.Username, "GitHost")
		if err == nil {
			data.Secret = secret
			data.OTPAuthURL = url
		}
	}

	h.render(w, http.StatusOK, "mfa_setup.tmpl", data)
}

// mfaEnable verifies a TOTP code and enables MFA.
func (h *Handler) mfaEnable(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/settings/mfa?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	code := strings.TrimSpace(r.PostFormValue("code"))
	secret := strings.TrimSpace(r.PostFormValue("secret"))

	data := struct {
		pageData
		MFAEnabled    bool
		UnusedCodes   int
		Secret        string
		OTPAuthURL    string
		FormError     string
		RecoveryCodes []string
	}{pageData: pd}

	if secret == "" || code == "" {
		data.FormError = "Secret and verification code are required."
		h.render(w, http.StatusBadRequest, "mfa_setup.tmpl", data)
		return
	}

	if !totp.Verify(code, secret) {
		data.FormError = "Invalid TOTP code. Check your authenticator app and try again."
		h.render(w, http.StatusBadRequest, "mfa_setup.tmpl", data)
		return
	}

	// Encrypt and store the secret, then enable MFA.
	encrypted, err := totp.EncryptSecret(secret, h.deps.InstanceSecret)
	if err != nil {
		data.FormError = "Failed to secure the TOTP secret."
		h.render(w, http.StatusInternalServerError, "mfa_setup.tmpl", data)
		return
	}
	if err := h.deps.Store.SetTOTPSecret(ctx, su.User.ID, encrypted); err != nil {
		data.FormError = "Failed to store the TOTP secret."
		h.render(w, http.StatusInternalServerError, "mfa_setup.tmpl", data)
		return
	}
	if err := h.deps.Store.SetMFAEnabled(ctx, su.User.ID, true); err != nil {
		data.FormError = "Failed to enable MFA."
		h.render(w, http.StatusInternalServerError, "mfa_setup.tmpl", data)
		return
	}

	// Generate recovery codes (shown exactly once).
	plaintext, hashed, err := totp.GenerateRecoveryCodes(10)
	if err != nil {
		data.FormError = "Failed to generate recovery codes."
		h.render(w, http.StatusInternalServerError, "mfa_setup.tmpl", data)
		return
	}
	if err := h.deps.Store.AddRecoveryCodes(ctx, su.User.ID, hashed); err != nil {
		data.FormError = "Failed to store recovery codes."
		h.render(w, http.StatusInternalServerError, "mfa_setup.tmpl", data)
		return
	}

	_ = h.deps.Store.Audit(ctx, &su.User.ID, "auth.mfa_enabled", su.User.Username, "web")

	// Render the page with recovery codes visible.
	data.MFAEnabled = true
	data.RecoveryCodes = plaintext
	data.UnusedCodes = 10
	h.render(w, http.StatusOK, "mfa_setup.tmpl", data)
}

// mfaDisable turns off MFA (requires a valid TOTP code).
func (h *Handler) mfaDisable(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/settings/mfa?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	code := strings.TrimSpace(r.PostFormValue("code"))

	encrypted, err := h.deps.Store.GetTOTPSecret(ctx, su.User.ID)
	if err != nil || encrypted == "" {
		http.Redirect(w, r, "/settings/mfa?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	secret, err := totp.DecryptSecret(encrypted, h.deps.InstanceSecret)
	if err != nil {
		http.Redirect(w, r, "/settings/mfa?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	if !totp.Verify(code, secret) {
		http.Redirect(w, r, "/settings/mfa?msg=err-bad-request", http.StatusSeeOther)
		return
	}

	_ = h.deps.Store.SetMFAEnabled(ctx, su.User.ID, false)
	_ = h.deps.Store.SetTOTPSecret(ctx, su.User.ID, "")
	_ = h.deps.Store.Audit(ctx, &su.User.ID, "auth.mfa_disabled", su.User.Username, "web")
	http.Redirect(w, r, "/settings/mfa?msg=mfa-disabled", http.StatusSeeOther)
}

// mfaChallenge renders the TOTP challenge during login.
// The login handler issued a session flagged mfa_pending (password
// passed, second factor outstanding); requireSession redirects every
// other page here until the challenge clears the flag.
func (h *Handler) mfaChallenge(w http.ResponseWriter, r *http.Request) {
	pd, su, _ := h.pageData(r)
	if su == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !su.Session.MFAPending {
		// Nothing pending (already verified, or MFA disabled): normal home.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data := struct {
		pageData
		FormError string
	}{pageData: pd}
	if msg := r.URL.Query().Get("msg"); msg == "err" {
		data.FormError = "Invalid code. Try again or use a recovery code."
	}
	h.render(w, http.StatusOK, "mfa_challenge.tmpl", data)
}

// mfaChallengeVerify checks the TOTP/recovery code and completes login.
func (h *Handler) mfaChallengeVerify(w http.ResponseWriter, r *http.Request) {
	pd, su, token := h.pageData(r)
	if su == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !su.Session.MFAPending {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/mfa/challenge?msg=err", http.StatusSeeOther)
		return
	}

	failServer := func(err error, where string) {
		h.logger.Error(where, "err", err)
		h.render(w, http.StatusInternalServerError, "mfa_challenge.tmpl", struct {
			pageData
			FormError string
		}{pageData: pd, FormError: "Second-factor check failed; try again."})
	}

	// Throttle second-factor guessing with the same policy as login,
	// under a distinct key so the post-password Reset cannot refresh it.
	key := "mfa|" + clientIP(r) + "|" + su.User.Username
	if !h.deps.Limiter.Allow(key) {
		_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "auth.mfa_rate_limited", su.User.Username, "web")
		http.Redirect(w, r, "/mfa/challenge?msg=err", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	code := strings.TrimSpace(r.PostFormValue("code"))
	verified := false

	// Try TOTP first. A database error must fail closed, not fall
	// through to the recovery-code path.
	encrypted, err := h.deps.Store.GetTOTPSecret(ctx, su.User.ID)
	switch {
	case err == nil && encrypted != "":
		secret, decErr := totp.DecryptSecret(encrypted, h.deps.InstanceSecret)
		if decErr == nil && totp.Verify(code, secret) {
			verified = true
		}
	case err == nil:
		// No TOTP secret configured; fall through to recovery codes.
	default:
		failServer(err, "mfa_secret_lookup_failed")
		return
	}

	// Try a recovery code when TOTP did not verify.
	if !verified {
		ok, err := h.deps.Store.TryRecoveryCode(ctx, su.User.ID, code)
		if err != nil {
			failServer(err, "mfa_recovery_lookup_failed")
			return
		}
		verified = ok
	}

	if !verified {
		http.Redirect(w, r, "/mfa/challenge?msg=err", http.StatusSeeOther)
		return
	}

	// Success: promote the parked session to a full one.
	if err := h.deps.Sessions.MarkMFAPending(ctx, token, false); err != nil {
		// Fail closed: the session stays parked, the user can retry.
		h.logger.Error("mfa_pending_clear_failed", "err", err)
		http.Redirect(w, r, "/mfa/challenge?msg=err", http.StatusSeeOther)
		return
	}
	h.deps.Limiter.Reset(key)
	_ = h.deps.Store.Audit(ctx, &su.User.ID, "auth.login_mfa_passed", su.User.Username, "web")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

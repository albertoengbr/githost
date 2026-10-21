package web

import (
	"net/http"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/session"
)

// passwordPage renders the change-password form.
func (h *Handler) passwordPage(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	data := struct {
		pageData
		FormError string
		FormOK    string
	}{pageData: pd}
	h.render(w, http.StatusOK, "password.tmpl", data)
}

// passwordSubmit changes the user's password. On success it invalidates
// ALL sessions for the user (including the current one) — the user must
// re-authenticate. This prevents a stolen session from surviving a
// password reset.
func (h *Handler) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	data := struct {
		pageData
		FormError string
		FormOK    string
	}{pageData: pd}

	fail := func(status int, msg string) {
		data.FormError = msg
		h.render(w, status, "password.tmpl", data)
	}

	if !h.verifyCSRF(r, su.Session.ID) {
		fail(http.StatusBadRequest, flashText("err-bad-csrf"))
		return
	}

	// Rate limit password change attempts too.
	key := clientIP(r) + "|pwchange|" + su.User.Username
	if !h.deps.Limiter.Allow(key) {
		fail(http.StatusTooManyRequests, "Too many attempts. Wait a few minutes.")
		return
	}

	current := r.PostFormValue("current")
	newPassword := r.PostFormValue("new_password")
	confirm := r.PostFormValue("confirm_password")

	// Verify the current password.
	ok, err := auth.VerifyPassword(current, su.User.PasswordHash)
	if err != nil || !ok {
		fail(http.StatusUnauthorized, "Current password is incorrect.")
		return
	}

	// Validate the new password.
	if newPassword != confirm {
		fail(http.StatusBadRequest, "New passwords do not match.")
		return
	}
	if err := auth.ValidatePasswordPolicy(newPassword); err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	if newPassword == current {
		fail(http.StatusBadRequest, "New password must differ from the current one.")
		return
	}

	// Hash and store.
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		h.logger.Error("hash_password_failed", "err", err)
		fail(http.StatusInternalServerError, "Failed to update password.")
		return
	}
	if err := h.deps.Store.SetPassword(r.Context(), su.User.ID, hash); err != nil {
		h.logger.Error("set_password_failed", "err", err)
		fail(http.StatusInternalServerError, "Failed to update password.")
		return
	}

	// Invalidate ALL sessions for this user — security first.
	if err := h.deps.Store.DeleteUserSessions(r.Context(), su.User.ID); err != nil {
		h.logger.Error("delete_sessions_failed", "err", err)
	}

	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "auth.password_changed", su.User.Username, "web")

	// Clear the cookie so the browser knows the session is gone.
	session.ClearCookie(w, r)
	http.Redirect(w, r, "/login?msg=password-changed", http.StatusSeeOther)
}

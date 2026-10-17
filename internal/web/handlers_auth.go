package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/session"
	"githost.dev/githost/internal/store"
)

// visitorCookie carries the CSRF binding for the (pre-login) login form.
const visitorCookie = "githost_visitor"

func (h *Handler) visitorBinding(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(visitorCookie); err == nil && c.Value != "" {
		return c.Value
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to a per-request value; the form simply expires.
		return fmt.Sprintf("visitor-%p", r)
	}
	value := hex.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name:     visitorCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	return value
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	pd, su, _ := h.pageData(r)
	if su != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	binding := h.visitorBinding(w, r)
	pd.CSRF = h.deps.CSRF.Token(binding)
	h.render(w, http.StatusOK, "login.tmpl", struct{ pageData }{pd})
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(visitorCookie)
	binding := "anonymous"
	if err == nil {
		binding = c.Value
	}
	pd := pageData{Username: "", CSRF: h.deps.CSRF.Token(binding)}

	fail := func(status int, message string) {
		pd.Flash = message
		pd.FlashKind = "err"
		h.render(w, status, "login.tmpl", struct{ pageData }{pd})
	}

	if !h.deps.CSRF.Verify(binding, r.PostFormValue("csrf")) {
		fail(http.StatusBadRequest, flashText("err-bad-csrf"))
		return
	}
	username := store.NormalizeUsername(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	if username == "" || password == "" {
		fail(http.StatusBadRequest, flashText("err-login"))
		return
	}

	// Throttle online guessing (SEC-003).
	key := clientIP(r) + "|" + username
	if !h.deps.Limiter.Allow(key) {
		_ = h.deps.Store.Audit(r.Context(), nil, "auth.login_rate_limited", username, "web")
		fail(http.StatusTooManyRequests, flashText("err-rate-login"))
		return
	}

	user, err := h.deps.Store.UserByUsername(r.Context(), username)
	if err != nil {
		fail(http.StatusUnauthorized, flashText("err-login"))
		return
	}
	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		_ = h.deps.Store.Audit(r.Context(), nil, "auth.login_failed", username, "web")
		fail(http.StatusUnauthorized, flashText("err-login"))
		return
	}
	if user.PasswordHash == "" {
		// Account exists but has no usable password yet.
		fail(http.StatusUnauthorized, flashText("err-login"))
		return
	}

	h.deps.Limiter.Reset(key)
	token, err := h.deps.Sessions.Start(r.Context(), user.ID, clientIP(r), r.UserAgent())
	if err != nil {
		h.logger.Error("session_start_failed", "err", err)
		fail(http.StatusInternalServerError, "Login failed; try again.")
		return
	}
	session.SetCookie(w, r, token, 0)
	_ = h.deps.Store.Audit(r.Context(), &user.ID, "auth.login", user.Username, "web")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	pd, su, token := h.pageData(r)
	if su != nil && !h.verifyCSRF(r, su.Session.ID) {
		pd.Flash = flashText("err-bad-csrf")
		pd.FlashKind = "err"
		h.render(w, http.StatusBadRequest, "login.tmpl", struct{ pageData }{pd})
		return
	}
	if err := h.deps.Sessions.End(r.Context(), token); err != nil {
		h.logger.Error("session_end_failed", "err", err)
	}
	session.ClearCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

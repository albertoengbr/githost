package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/store"
)

// adminUserView is the list-row projection (password hashes never reach
// templates).
type adminUserView struct {
	ID        int64
	Username  string
	Email     string
	IsAdmin   bool
	CreatedAt string
	RepoCount int
}

// requireAdmin resolves the session and restricts the route to instance
// administrators. Non-admins get 404 — the admin area does not confirm
// its own existence (same policy as private repositories).
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (pageData, *store.SessionUser, bool) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return pd, nil, false
	}
	if !su.User.IsAdmin {
		http.NotFound(w, r)
		return pd, nil, false
	}
	return pd, su, true
}

// toAdminUserView projects one user plus its repository count.
func (h *Handler) toAdminUserView(ctx context.Context, u *store.User) adminUserView {
	repos, err := h.deps.Store.RepositoriesByOwner(ctx, u.ID)
	count := 0
	if err == nil {
		count = len(repos)
	}
	return adminUserView{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		IsAdmin:   u.IsAdmin,
		CreatedAt: u.CreatedAt.Format("2006-01-02"),
		RepoCount: count,
	}
}

// adminUsersPage lists every account with edit/delete actions.
func (h *Handler) adminUsersPage(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	users, err := h.deps.Store.Users(r.Context())
	if err != nil {
		h.logger.Error("admin_user_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var views []adminUserView
	for _, u := range users {
		views = append(views, h.toAdminUserView(r.Context(), u))
	}
	data := struct {
		pageData
		Users     []adminUserView
		CurrentID int64
		FormError string
	}{pageData: pd, Users: views, CurrentID: su.User.ID}
	h.render(w, http.StatusOK, "admin_users.tmpl", data)
}

// adminUserNewPage shows the account creation form.
func (h *Handler) adminUserNewPage(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	data := struct {
		pageData
		FormError string
		Username  string
		Email     string
	}{pageData: pd}
	h.render(w, http.StatusOK, "admin_user_new.tmpl", data)
}

// adminUserCreate provisions an account (public registration stays off;
// only an operator/admin can create accounts).
func (h *Handler) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/admin/users?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}

	username := store.NormalizeUsername(strings.TrimSpace(r.PostFormValue("username")))
	email := strings.TrimSpace(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm_password")
	isAdmin := r.PostFormValue("is_admin") == "1"

	renderErr := func(status int, msg string) {
		data := struct {
			pageData
			FormError string
			Username  string
			Email     string
		}{pageData: pd, FormError: msg, Username: username, Email: email}
		h.render(w, status, "admin_user_new.tmpl", data)
	}

	if err := store.ValidateUsername(username); err != nil {
		renderErr(http.StatusBadRequest, err.Error())
		return
	}
	if err := auth.ValidatePasswordPolicy(password); err != nil {
		renderErr(http.StatusBadRequest, err.Error())
		return
	}
	if password != confirm {
		renderErr(http.StatusBadRequest, "Passwords do not match.")
		return
	}
	if _, err := h.deps.Store.UserByUsername(r.Context(), username); err == nil {
		renderErr(http.StatusBadRequest, "That username is already taken.")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		h.logger.Error("hash_password_failed", "err", err)
		renderErr(http.StatusInternalServerError, "Could not create the account.")
		return
	}
	if _, err := h.deps.Store.CreateUser(r.Context(), username, email, hash, isAdmin); err != nil {
		h.logger.Error("admin_user_create_failed", "err", err)
		renderErr(http.StatusBadRequest, err.Error())
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "user.created", username, "web")
	http.Redirect(w, r, "/admin/users?msg=user-created", http.StatusSeeOther)
}

// adminUserEditPage shows the edit form for one account.
func (h *Handler) adminUserEditPage(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, err := h.deps.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("admin_user_lookup_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := struct {
		pageData
		User      adminUserView
		FormError string
		FormOK    string
	}{pageData: pd, User: h.toAdminUserView(r.Context(), u)}
	h.render(w, http.StatusOK, "admin_user_edit.tmpl", data)
}

// adminUserUpdate saves the edit form: email, admin flag and an optional
// password reset (which invalidates the target's sessions).
func (h *Handler) adminUserUpdate(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := h.deps.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("admin_user_lookup_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	back := "/admin/users/edit/" + strconv.FormatInt(id, 10)
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, back+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}

	email := strings.TrimSpace(r.PostFormValue("email"))
	isAdmin := r.PostFormValue("is_admin") == "1"

	renderErr := func(msg string) {
		data := struct {
			pageData
			User      adminUserView
			FormError string
			FormOK    string
		}{pageData: pd, User: h.toAdminUserView(r.Context(), target), FormError: msg}
		h.render(w, http.StatusBadRequest, "admin_user_edit.tmpl", data)
	}

	// Never leave the instance without an administrator — including a
	// self-demotion by the last remaining admin (the classic lockout).
	if target.IsAdmin && !isAdmin {
		admins, err := h.deps.Store.CountAdmins(r.Context())
		if err == nil && admins <= 1 {
			renderErr("This is the only administrator — promote another account first.")
			return
		}
	}

	if err := h.deps.Store.UpdateUserAccount(r.Context(), target.ID, email, isAdmin); err != nil {
		h.logger.Error("admin_user_update_failed", "err", err)
		renderErr("Could not save the account.")
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "user.updated", target.Username, "web")
	http.Redirect(w, r, back+"?msg=user-updated", http.StatusSeeOther)
}

// adminUserSetPassword resets another account's password (admin action).
// A reset always invalidates every live session of the target account.
func (h *Handler) adminUserSetPassword(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := h.deps.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("admin_user_lookup_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	back := "/admin/users/edit/" + strconv.FormatInt(id, 10)
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, back+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}

	renderErr := func(msg string) {
		data := struct {
			pageData
			User      adminUserView
			FormError string
			FormOK    string
		}{pageData: pd, User: h.toAdminUserView(r.Context(), target), FormError: msg}
		h.render(w, http.StatusBadRequest, "admin_user_edit.tmpl", data)
	}

	newPassword := r.PostFormValue("new_password")
	confirm := r.PostFormValue("confirm_password")
	if err := auth.ValidatePasswordPolicy(newPassword); err != nil {
		renderErr(err.Error())
		return
	}
	if newPassword != confirm {
		renderErr("Passwords do not match.")
		return
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		h.logger.Error("hash_password_failed", "err", err)
		renderErr("Could not set the password.")
		return
	}
	if err := h.deps.Store.SetPassword(r.Context(), target.ID, hash); err != nil {
		h.logger.Error("admin_set_password_failed", "err", err)
		renderErr("Could not set the password.")
		return
	}
	if err := h.deps.Store.DeleteUserSessions(r.Context(), target.ID); err != nil {
		h.logger.Error("admin_delete_sessions_failed", "err", err)
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "user.password_reset", target.Username, "web")
	http.Redirect(w, r, back+"?msg=user-updated", http.StatusSeeOther)
}

// adminUserDelete removes an account. Guarded so the instance always
// keeps an admin and repositories are never orphaned by the cascade.
func (h *Handler) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if su.User.ID == id {
		pd.Flash = "You cannot delete your own account."
		pd.FlashKind = "err"
		users, _ := h.deps.Store.Users(r.Context())
		var views []adminUserView
		for _, u := range users {
			views = append(views, h.toAdminUserView(r.Context(), u))
		}
		h.render(w, http.StatusBadRequest, "admin_users.tmpl", struct {
			pageData
			Users     []adminUserView
			CurrentID int64
			FormError string
		}{pageData: pd, Users: views, CurrentID: su.User.ID, FormError: pd.Flash})
		return
	}

	// CSRF checked after the self-delete guard so the message is specific.
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/admin/users?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}

	renderListErr := func(msg string) {
		users, _ := h.deps.Store.Users(r.Context())
		var views []adminUserView
		for _, u := range users {
			views = append(views, h.toAdminUserView(r.Context(), u))
		}
		h.render(w, http.StatusBadRequest, "admin_users.tmpl", struct {
			pageData
			Users     []adminUserView
			CurrentID int64
			FormError string
		}{pageData: pd, Users: views, CurrentID: su.User.ID, FormError: msg})
	}

	target, err := h.deps.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("admin_user_lookup_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if target.IsAdmin {
		admins, err := h.deps.Store.CountAdmins(r.Context())
		if err == nil && admins <= 1 {
			renderListErr("This is the only administrator — promote another account first.")
			return
		}
	}
	if repos, err := h.deps.Store.RepositoriesByOwner(r.Context(), target.ID); err == nil && len(repos) > 0 {
		renderListErr("The account still owns " + strconv.Itoa(len(repos)) +
			" repository/repositories — delete or transfer them first.")
		return
	}

	if err := h.deps.Store.DeleteUser(r.Context(), target.ID); err != nil {
		h.logger.Error("admin_user_delete_failed", "err", err)
		renderListErr("Cannot remove the account — it has history (issues, pull requests, releases) that references it.")
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "user.deleted", target.Username, "web")
	http.Redirect(w, r, "/admin/users?msg=user-deleted", http.StatusSeeOther)
}

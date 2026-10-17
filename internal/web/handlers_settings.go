package web

import (
	"net/http"
	"strconv"
	"strings"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/store"
)

// ------------------------------------------------------------- user keys

func (h *Handler) keysPage(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	keys, err := h.deps.Store.SSHKeysByUser(r.Context(), su.User.ID)
	if err != nil {
		h.logger.Error("keys_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := struct {
		pageData
		Keys []*store.SSHKey
	}{pageData: pd, Keys: keys}
	h.render(w, http.StatusOK, "settings_keys.tmpl", data)
}

func (h *Handler) keyAdd(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/settings/keys?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	canonical, fingerprint, err := auth.ParseSSHPublicKey(r.PostFormValue("key"))
	if err != nil {
		http.Redirect(w, r, "/settings/keys?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	if _, err := h.deps.Store.AddSSHKey(r.Context(), su.User.ID,
		fingerprint, canonical, strings.TrimSpace(r.PostFormValue("title"))); err != nil {
		http.Redirect(w, r, "/settings/keys?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "key.added", fingerprint, "web")
	http.Redirect(w, r, "/settings/keys?msg=key-added", http.StatusSeeOther)
}

func (h *Handler) keyDelete(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/settings/keys?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/settings/keys?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	if err := h.deps.Store.DeleteSSHKey(r.Context(), id, su.User.ID); err != nil {
		h.logger.Error("key_delete_failed", "err", err)
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "key.removed", strconv.FormatInt(id, 10), "web")
	http.Redirect(w, r, "/settings/keys?msg=key-removed", http.StatusSeeOther)
}

// ----------------------------------------------------------- user tokens

type tokenView struct {
	ID          int64
	Name        string
	TokenPrefix string
	Scopes      string
	CreatedAt   string
}

func (h *Handler) tokensPage(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tokens, err := h.deps.Store.TokensByUser(r.Context(), su.User.ID)
	if err != nil {
		h.logger.Error("tokens_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var views []tokenView
	for _, t := range tokens {
		if !t.Active() {
			continue
		}
		views = append(views, tokenView{
			ID:          t.ID,
			Name:        t.Name,
			TokenPrefix: t.TokenPrefix,
			Scopes:      t.Scopes,
			CreatedAt:   t.CreatedAt.Format("2006-01-02"),
		})
	}
	data := struct {
		pageData
		Tokens   []tokenView
		NewToken string
	}{pageData: pd, Tokens: views}
	h.render(w, http.StatusOK, "settings_tokens.tmpl", data)
}

func (h *Handler) tokenCreate(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/settings/tokens?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	var scopes []string
	if r.PostFormValue("scope_read") == "1" {
		scopes = append(scopes, auth.ScopeRepoRead)
	}
	if r.PostFormValue("scope_write") == "1" {
		scopes = append(scopes, auth.ScopeRepoWrite)
	}
	scopesStr := strings.Join(scopes, " ")

	token, tokenHash, prefix, err := auth.GenerateToken()
	if err != nil {
		http.Redirect(w, r, "/settings/tokens?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	if _, err := h.deps.Store.CreateToken(r.Context(), su.User.ID,
		strings.TrimSpace(r.PostFormValue("name")), tokenHash, prefix, scopesStr, nil); err != nil {
		http.Redirect(w, r, "/settings/tokens?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "token.created", su.User.Username, "web "+scopesStr)

	// Render immediately with the plaintext token — it is shown exactly
	// once and never travels through URLs or logs.
	tokens, _ := h.deps.Store.TokensByUser(r.Context(), su.User.ID)
	var views []tokenView
	for _, t := range tokens {
		if !t.Active() {
			continue
		}
		views = append(views, tokenView{
			ID: t.ID, Name: t.Name, TokenPrefix: t.TokenPrefix,
			Scopes: t.Scopes, CreatedAt: t.CreatedAt.Format("2006-01-02"),
		})
	}
	data := struct {
		pageData
		Tokens   []tokenView
		NewToken string
	}{pageData: pd, Tokens: views, NewToken: token}
	h.render(w, http.StatusOK, "settings_tokens.tmpl", data)
}

func (h *Handler) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/settings/tokens?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/settings/tokens?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	if err := h.deps.Store.RevokeToken(r.Context(), id, su.User.ID); err != nil {
		h.logger.Error("token_revoke_failed", "err", err)
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "token.revoked", strconv.FormatInt(id, 10), "web")
	http.Redirect(w, r, "/settings/tokens?msg=token-revoked", http.StatusSeeOther)
}

// --------------------------------------------------------- repo settings

func (h *Handler) repoSettings(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !rc.canManage {
		http.NotFound(w, r)
		return
	}
	branches, _ := h.deps.Git.ListBranches(r.Context(), rc.diskPath)
	data := struct {
		pageData
		Repo     repoView
		Branches []git.Ref
	}{pageData: pd, Repo: rc.view, Branches: branches}
	h.render(w, http.StatusOK, "repo_settings.tmpl", data)
}

func (h *Handler) repoSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	back := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/settings"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, back+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	if !rc.canManage {
		http.NotFound(w, r)
		return
	}
	ownerName := rc.repo.Owner
	repoName := rc.repo.Name

	visibility := r.PostFormValue("visibility")
	if err := store.ValidateVisibility(visibility); err != nil {
		http.NotFound(w, r)
		return
	}
	newName := strings.TrimSpace(r.PostFormValue("new_name"))
	if newName != "" && newName != repoName {
		if err := store.ValidateRepositoryName(newName); err != nil {
			http.Redirect(w, r, "/"+ownerName+"/"+repoName+"/settings?msg=err-bad-request", http.StatusSeeOther)
			return
		}
		repoName = newName
	}
	branch := r.PostFormValue("default_branch")
	if err := git.ValidateBranchName(branch); err != nil {
		http.Redirect(w, r, "/"+ownerName+"/"+repoName+"/settings?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	archived := r.PostFormValue("archived") == "1"

	if branch != rc.repo.DefaultBranch {
		if err := h.deps.Git.SetDefaultBranch(r.Context(), rc.diskPath, branch); err != nil {
			http.Redirect(w, r, "/"+ownerName+"/"+repoName+"/settings?msg=err-bad-request", http.StatusSeeOther)
			return
		}
	}
	err := h.deps.Store.UpdateRepository(r.Context(), rc.repo.ID,
		&repoName, strPtrT(strings.TrimSpace(r.PostFormValue("description"))),
		&visibility, &branch, &archived)
	if err != nil {
		h.logger.Error("repo_update_failed", "err", err)
		http.Redirect(w, r, "/"+ownerName+"/"+rc.repo.Name+"/settings?msg=err-bad-request", http.StatusSeeOther)
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "repo.updated", ownerName+"/"+repoName, "web")
	http.Redirect(w, r, "/"+ownerName+"/"+repoName+"/settings?msg=repo-updated", http.StatusSeeOther)
}

func (h *Handler) repoDelete(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !rc.canManage || !h.verifyCSRF(r, su.Session.ID) || r.PostFormValue("confirm") != "yes" {
		http.NotFound(w, r)
		return
	}
	target := rc.repo.Owner + "/" + rc.repo.Name
	if err := h.deps.Repos.Delete(r.Context(), rc.repo); err != nil {
		h.logger.Error("repo_delete_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "repo.deleted", target, "web")
	http.Redirect(w, r, "/?msg=repo-deleted", http.StatusSeeOther)
}

func strPtrT(s string) *string { return &s }

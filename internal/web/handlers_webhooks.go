package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"githost.dev/githost/internal/store"
)

// webhookView pairs a hook with its most recent deliveries for the page.
type webhookView struct {
	Hook       *store.Webhook
	Deliveries []*store.WebhookDelivery
}

// validWebhookURLShape performs the cheap registration-time checks. The
// authoritative SSRF guard is the dial-time Control hook in
// internal/webhooks (it sees the resolved IP), so no DNS is needed here
// — an offline instance can still register webhooks for later.
func validWebhookURLShape(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "" || u.User != nil {
		return false
	}
	return true
}

// requireRepoManager resolves the repo and requires admin level,
// replying 404 (never 403 — existence is not disclosed).
func (h *Handler) requireRepoManager(w http.ResponseWriter, r *http.Request) (*repoContext, bool) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return nil, false
	}
	if !rc.canManage {
		http.NotFound(w, r)
		return nil, false
	}
	return rc, true
}

func (h *Handler) webhooksPage(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	hooks, err := h.deps.Store.WebhooksByRepository(r.Context(), rc.repo.ID)
	if err != nil {
		h.logger.Error("webhook_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]webhookView, 0, len(hooks))
	for _, hook := range hooks {
		deliveries, err := h.deps.Store.RecentDeliveries(r.Context(), hook.ID, 5)
		if err != nil {
			deliveries = nil
		}
		views = append(views, webhookView{Hook: hook, Deliveries: deliveries})
	}
	data := struct {
		pageData
		Repo      repoView
		ActiveTab string
		CanManage bool
		Hooks     []webhookView
		FormError string
		FormURL   string
	}{pageData: pd, Repo: rc.view, ActiveTab: "settings", CanManage: rc.canManage,
		Hooks: views}
	h.render(w, http.StatusOK, "webhook_list.tmpl", data)
}

func (h *Handler) webhookCreate(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	base := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/settings/webhooks"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, base+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}

	hookURL := strings.TrimSpace(r.PostFormValue("url"))
	secret := strings.TrimSpace(r.PostFormValue("secret"))
	events := r.PostForm["events"]

	renderErr := func(msg string) {
		hooks, _ := h.deps.Store.WebhooksByRepository(r.Context(), rc.repo.ID)
		views := make([]webhookView, 0, len(hooks))
		for _, hook := range hooks {
			views = append(views, webhookView{Hook: hook})
		}
		data := struct {
			pageData
			Repo      repoView
			ActiveTab string
			CanManage bool
			Hooks     []webhookView
			FormError string
			FormURL   string
		}{pageData: pd, Repo: rc.view, ActiveTab: "settings", CanManage: rc.canManage,
			Hooks: views, FormError: msg, FormURL: hookURL}
		h.render(w, http.StatusBadRequest, "webhook_list.tmpl", data)
	}

	if !validWebhookURLShape(hookURL) {
		renderErr("URL must be an absolute http(s) address without credentials.")
		return
	}
	if len(secret) > 0 && len(secret) < 16 {
		renderErr("Secret must be at least 16 characters (or empty to disable signing).")
		return
	}
	if len(events) == 0 {
		renderErr("Pick at least one event to deliver.")
		return
	}
	for _, e := range events {
		switch e {
		case "push", "issues", "pulls":
		default:
			renderErr("Unknown event selection.")
			return
		}
	}

	if _, err := h.deps.Store.CreateWebhook(r.Context(), rc.repo.ID,
		hookURL, secret, strings.Join(events, ",")); err != nil {
		renderErr(err.Error())
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "webhook.created",
		rc.repo.Owner+"/"+rc.repo.Name, "web")
	http.Redirect(w, r, base+"?msg=webhook-created", http.StatusSeeOther)
}

func (h *Handler) webhookToggle(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	_ = pd
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	base := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/settings/webhooks"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, base+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	hookID, _ := strconv.ParseInt(r.PostFormValue("hook_id"), 10, 64)
	active := r.PostFormValue("active") == "1"
	if err := h.deps.Store.SetWebhookActive(r.Context(), rc.repo.ID, hookID, active); err != nil {
		h.logger.Error("webhook_toggle_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, base, http.StatusSeeOther)
}

func (h *Handler) webhookDelete(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	_ = pd
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	base := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/settings/webhooks"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, base+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	hookID, _ := strconv.ParseInt(r.PostFormValue("hook_id"), 10, 64)
	if err := h.deps.Store.DeleteWebhook(r.Context(), rc.repo.ID, hookID); err != nil {
		h.logger.Error("webhook_delete_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "webhook.deleted",
		rc.repo.Owner+"/"+rc.repo.Name, "web")
	http.Redirect(w, r, base+"?msg=webhook-deleted", http.StatusSeeOther)
}

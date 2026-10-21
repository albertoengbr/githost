package web

import (
	"net/http"
	"strconv"
	"strings"

	"githost.dev/githost/internal/store"
)

// labelsPage lists a repository's labels with create/delete controls.
func (h *Handler) labelsPage(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	labels, err := h.deps.Store.Labels(r.Context(), rc.repo.ID)
	if err != nil {
		h.logger.Error("label_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := struct {
		pageData
		Repo      repoView
		ActiveTab string
		CanManage bool
		Labels    []store.Label
		FormError string
	}{pageData: pd, Repo: rc.view, ActiveTab: "settings", CanManage: rc.canManage,
		Labels: labels}
	h.render(w, http.StatusOK, "labels.tmpl", data)
}

// labelCreate adds a label (name + 6-hex color).
func (h *Handler) labelCreate(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	base := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/settings/labels"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, base+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	color := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(r.PostFormValue("color"))), "#")

	if _, err := h.deps.Store.CreateLabel(r.Context(), rc.repo.ID, name, color); err != nil {
		h.logger.Error("label_create_failed", "err", err)
		pd.Flash = "Label needs a 1-50 character name and a 6-hex color (e.g. 1e5a87)."
		pd.FlashKind = "err"
		labels, _ := h.deps.Store.Labels(r.Context(), rc.repo.ID)
		h.render(w, http.StatusBadRequest, "labels.tmpl", struct {
			pageData
			Repo      repoView
			ActiveTab string
			CanManage bool
			Labels    []store.Label
			FormError string
		}{pageData: pd, Repo: rc.view, ActiveTab: "settings", CanManage: rc.canManage,
			Labels: labels, FormError: pd.Flash})
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "label.created",
		rc.repo.Owner+"/"+rc.repo.Name+":"+name, "web")
	http.Redirect(w, r, base+"?msg=label-created", http.StatusSeeOther)
}

// labelDelete removes a label; issue associations cascade.
func (h *Handler) labelDelete(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	_ = pd
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	base := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/settings/labels"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, base+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	labelID, _ := strconv.ParseInt(r.PostFormValue("label_id"), 10, 64)
	if err := h.deps.Store.DeleteLabel(r.Context(), rc.repo.ID, labelID); err != nil {
		h.logger.Error("label_delete_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, base+"?msg=label-deleted", http.StatusSeeOther)
}

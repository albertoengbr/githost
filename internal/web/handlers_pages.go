package web

import (
	"net/http"
	"strings"
	"time"

	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// repoView is the repository projection templates receive. Raw store rows
// never reach templates.
type repoView struct {
	Owner         string
	Name          string
	Description   string
	Visibility    string
	DefaultBranch string
	IsArchived    bool
	UpdatedAt     time.Time
}

func toRepoView(r *store.Repository) repoView {
	return repoView{
		Owner:         r.Owner,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    r.Visibility,
		DefaultBranch: r.DefaultBranch,
		IsArchived:    r.IsArchived,
		UpdatedAt:     r.UpdatedAt,
	}
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	pd, su, _ := h.pageData(r)
	var viewer *store.User
	if su != nil {
		viewer = &su.User
	}
	repos, err := h.deps.Store.RepositoriesVisibleTo(r.Context(), viewer)
	if err != nil {
		h.logger.Error("dashboard_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := struct {
		pageData
		Repos []repoView
	}{pageData: pd}
	for _, repo := range repos {
		data.Repos = append(data.Repos, toRepoView(repo))
	}
	h.render(w, http.StatusOK, "dashboard.tmpl", data)
}

func (h *Handler) repoNewPage(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	data := struct {
		pageData
		FormError string
	}{pageData: pd}
	h.render(w, http.StatusOK, "repo_new.tmpl", data)
}

func (h *Handler) repoNewSubmit(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, "/new?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	repo, err := h.deps.Repos.Create(r.Context(), &su.User, r.PostFormValue("name"), reposervice.CreateOptions{
		Description:   strings.TrimSpace(r.PostFormValue("description")),
		Visibility:    r.PostFormValue("visibility"),
		DefaultBranch: r.PostFormValue("default_branch"),
	})
	data := struct {
		pageData
		FormError string
	}{pageData: pd}
	if err != nil {
		data.FormError = err.Error()
		h.render(w, http.StatusBadRequest, "repo_new.tmpl", data)
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "repo.created", repo.Owner+"/"+repo.Name, "web")
	http.Redirect(w, r, "/"+repo.Owner+"/"+repo.Name+"?msg=repo-created", http.StatusSeeOther)
}

func (h *Handler) profilePage(w http.ResponseWriter, r *http.Request) {
	pd, su, _ := h.pageData(r)
	ownerName := store.NormalizeUsername(r.PathValue("owner"))
	owner, err := h.deps.Store.UserByUsername(r.Context(), ownerName)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	repos, err := h.deps.Store.RepositoriesByOwner(r.Context(), owner.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var viewer *store.User
	if su != nil {
		viewer = &su.User
	}
	data := struct {
		pageData
		Owner      string
		OwnerEmail string
		Repos      []repoView
	}{pageData: pd, Owner: owner.Username, OwnerEmail: owner.Email}
	for _, repo := range repos {
		// Per-repository authorization decides visibility.
		level, err := h.deps.Perms.ForRepository(r.Context(), viewer, repo)
		if err == nil && level >= permissions.Read {
			data.Repos = append(data.Repos, toRepoView(repo))
		}
	}
	h.render(w, http.StatusOK, "profile.tmpl", data)
}

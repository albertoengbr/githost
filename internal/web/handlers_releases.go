package web

import (
	"html/template"
	"net/http"
	"strings"

	"githost.dev/githost/internal/markdown"
	"githost.dev/githost/internal/store"
)

// releaseView is a release with its notes rendered to safe HTML.
type releaseView struct {
	Release   *store.Release
	NotesHTML template.HTML
}

// releasesPage lists a repository's releases. Drafts are only shown to
// repository managers.
func (h *Handler) releasesPage(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	releases, err := h.deps.Store.ReleasesByRepository(r.Context(), rc.repo.ID)
	if err != nil {
		h.logger.Error("release_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]releaseView, 0, len(releases))
	for _, rel := range releases {
		notes, err := markdown.Render(rel.NotesMD)
		if err != nil {
			notes = ""
		}
		views = append(views, releaseView{Release: rel, NotesHTML: notes})
	}
	data := struct {
		pageData
		Repo      repoView
		ActiveTab string
		CanManage bool
		Releases  []releaseView
	}{pageData: pd, Repo: rc.view, ActiveTab: "releases", CanManage: rc.canManage,
		Releases: views}
	h.render(w, http.StatusOK, "release_list.tmpl", data)
}

// releaseNew shows the creation form backed by the repository's tags.
func (h *Handler) releaseNew(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	tags, _ := h.deps.Git.ListTags(r.Context(), rc.diskPath)
	data := struct {
		pageData
		Repo      repoView
		ActiveTab string
		CanManage bool
		Tags      []struct{ Short string }
		FormError string
	}{pageData: pd, Repo: rc.view, ActiveTab: "releases", CanManage: rc.canManage}
	for _, t := range tags {
		data.Tags = append(data.Tags, struct{ Short string }{Short: t.Short})
	}
	h.render(w, http.StatusOK, "release_new.tmpl", data)
}

// releaseCreate records a release for an existing tag.
func (h *Handler) releaseCreate(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.requireRepoManager(w, r)
	if !ok {
		return
	}
	base := "/" + rc.repo.Owner + "/" + rc.repo.Name + "/releases"
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, base+"/new?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}

	tag := strings.TrimSpace(r.PostFormValue("tag"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	notes := strings.TrimSpace(r.PostFormValue("notes"))
	draft := r.PostFormValue("draft") == "1"

	// The tag must exist in the repository: store.CreateRelease trusts
	// the caller to verify that.
	exists := false
	if tags, err := h.deps.Git.ListTags(r.Context(), rc.diskPath); err == nil {
		for _, t := range tags {
			if t.Short == tag {
				exists = true
				break
			}
		}
	}
	if !exists {
		tags, _ := h.deps.Git.ListTags(r.Context(), rc.diskPath)
		data := struct {
			pageData
			Repo      repoView
			ActiveTab string
			CanManage bool
			Tags      []struct{ Short string }
			FormError string
		}{pageData: pd, Repo: rc.view, ActiveTab: "releases", CanManage: rc.canManage,
			FormError: "Pick an existing tag."}
		for _, t := range tags {
			data.Tags = append(data.Tags, struct{ Short string }{Short: t.Short})
		}
		h.render(w, http.StatusBadRequest, "release_new.tmpl", data)
		return
	}

	if _, err := h.deps.Store.CreateRelease(r.Context(), rc.repo.ID, su.User.ID,
		tag, name, notes, draft); err != nil {
		h.logger.Error("release_create_failed", "err", err)
		pd.Flash = "Could not create the release."
		pd.FlashKind = "err"
		h.render(w, http.StatusInternalServerError, "release_new.tmpl", struct {
			pageData
			Repo      repoView
			ActiveTab string
			CanManage bool
		}{pageData: pd, Repo: rc.view, ActiveTab: "releases", CanManage: rc.canManage})
		return
	}
	_ = h.deps.Store.Audit(r.Context(), &su.User.ID, "release.created",
		rc.repo.Owner+"/"+rc.repo.Name+"@"+tag, "web")
	http.Redirect(w, r, base+"?msg=release-created", http.StatusSeeOther)
}

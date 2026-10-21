package web

import (
	"net/http"
	"strings"

	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// searchPage renders the global search: repositories and issues the
// viewer is allowed to see. Anonymous visitors only ever match public
// content — the permission filter, not the query, decides visibility.
func (h *Handler) searchPage(w http.ResponseWriter, r *http.Request) {
	pd, su, _ := h.pageData(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	scope := r.URL.Query().Get("in")
	if scope != "repos" && scope != "issues" {
		scope = "all"
	}
	var viewer *store.User
	if su != nil {
		viewer = &su.User
	}

	data := struct {
		pageData
		Query  string
		Scope  string
		Repos  []repoView
		Issues []store.SearchResult
	}{pageData: pd, Query: query, Scope: scope}

	if query == "" {
		h.render(w, http.StatusOK, "search.tmpl", data)
		return
	}

	if scope == "all" || scope == "repos" {
		repos, err := h.deps.Store.SearchRepos(r.Context(), query, 50)
		if err != nil {
			h.logger.Error("search_repos_failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, repo := range repos {
			level, err := h.deps.Perms.ForRepository(r.Context(), viewer, repo)
			if err == nil && level >= permissions.Read {
				data.Repos = append(data.Repos, toRepoView(repo))
			}
		}
	}

	if scope == "all" || scope == "issues" {
		results, err := h.deps.Store.SearchIssuesAllRepos(r.Context(), query, 50)
		if err != nil {
			h.logger.Error("search_issues_failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		visible := map[string]bool{}
		for _, res := range results {
			if res.Repo == "" {
				continue
			}
			ok, known := visible[res.Repo]
			if !known {
				owner, name, _ := strings.Cut(res.Repo, "/")
				repo, err := h.deps.Repos.Resolve(r.Context(), owner, name)
				if err != nil {
					if err == reposervice.ErrNotFound {
						visible[res.Repo] = false
						continue
					}
					h.logger.Error("search_resolve_failed", "repo", res.Repo, "err", err)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				level, err := h.deps.Perms.ForRepository(r.Context(), viewer, repo)
				ok = err == nil && level >= permissions.Read
				visible[res.Repo] = ok
			}
			if ok {
				data.Issues = append(data.Issues, res)
			}
		}
	}

	h.render(w, http.StatusOK, "search.tmpl", data)
}

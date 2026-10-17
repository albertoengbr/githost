// Package api implements GitHost's versioned REST API (/api/v1).
//
// Conventions (docs/api/openapi.yaml is the contract):
//   - Authentication: "Authorization: token <value>" (also accepts Bearer);
//     access tokens only, with scopes (repo:read, repo:write, admin).
//   - Errors: {"error": {"code": "...", "message": "..."}} with proper HTTP
//     status codes; 404 is used both for missing repositories and for
//     private repositories the caller cannot see (existence is not leaked).
//   - Pagination: ?page=1&per_page=30 (per_page capped at 100).
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// Deps wires the API to the service graph.
type Deps struct {
	Store  *store.Store
	Perms  *permissions.Service
	Repos  *reposervice.Service
	Git    *git.Executor
	Logger *slog.Logger
}

// API serves /api/v1.
type API struct {
	deps   Deps
	logger *slog.Logger
}

// Route documents one registered endpoint (used by the contract test to
// stay in sync with docs/api/openapi.yaml).
type Route struct {
	Method  string
	Pattern string
}

// Routes returns every endpoint the API registers, in registration order.
func (a *API) Routes() []Route {
	return []Route{
		{"GET", "/api/v1/user"},
		{"GET", "/api/v1/user/repos"},
		{"GET", "/api/v1/repos"},
		{"POST", "/api/v1/repos"},
		{"GET", "/api/v1/repos/{owner}/{repo}"},
		{"PATCH", "/api/v1/repos/{owner}/{repo}"},
		{"DELETE", "/api/v1/repos/{owner}/{repo}"},
		{"GET", "/api/v1/repos/{owner}/{repo}/branches"},
		{"GET", "/api/v1/repos/{owner}/{repo}/tags"},
		{"GET", "/api/v1/repos/{owner}/{repo}/commits"},
	}
}

// Register mounts the API on mux.
func Register(mux *http.ServeMux, deps Deps) *API {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	a := &API{deps: deps, logger: deps.Logger}
	mux.HandleFunc("GET /api/v1/user", a.handleCurrentUser)
	mux.HandleFunc("GET /api/v1/user/repos", a.handleUserRepos)
	mux.HandleFunc("GET /api/v1/repos", a.handleListRepos)
	mux.HandleFunc("POST /api/v1/repos", a.handleCreateRepo)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}", a.handleGetRepo)
	mux.HandleFunc("PATCH /api/v1/repos/{owner}/{repo}", a.handleUpdateRepo)
	mux.HandleFunc("DELETE /api/v1/repos/{owner}/{repo}", a.handleDeleteRepo)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/branches", a.handleBranches)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/tags", a.handleTags)
	mux.HandleFunc("GET /api/v1/repos/{owner}/{repo}/commits", a.handleCommits)
	return a
}

// ------------------------------------------------------------ auth plumbing

type principal struct {
	user  *store.User
	token *store.AccessToken
}

// authenticate extracts and validates the bearer token. Returns
// (nil, ok=true) when no token was presented (anonymous).
func (a *API) authenticate(r *http.Request) (*principal, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, true
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || (parts[0] != "token" && parts[0] != "Bearer") {
		return nil, false
	}
	token, err := a.deps.Store.TokenByHash(r.Context(), store.HashToken(parts[1]))
	if err != nil || !token.Active() {
		return nil, false
	}
	user, err := a.deps.Store.UserByID(r.Context(), token.UserID)
	if err != nil {
		return nil, false
	}
	return &principal{user: user, token: token}, true
}

// requireScope authenticates and enforces a token scope.
func (a *API) requireScope(w http.ResponseWriter, r *http.Request, scope string) (*principal, bool) {
	p, ok := a.authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return nil, false
	}
	if p == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}
	if !auth.HasScope(p.token.Scopes, scope) {
		writeError(w, http.StatusForbidden, "forbidden",
			fmt.Sprintf("token lacks the %q scope", scope))
		return nil, false
	}
	return p, true
}

// resolveRepo resolves owner/repo enforcing the read-privacy policy.
func (a *API) resolveRepo(w http.ResponseWriter, r *http.Request, p *principal) (*store.Repository, string, bool) {
	owner := store.NormalizeUsername(r.PathValue("owner"))
	name := strings.TrimSuffix(r.PathValue("repo"), ".git")
	repo, err := a.deps.Repos.Resolve(r.Context(), owner, name)
	if errors.Is(err, reposervice.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, "", false
	}
	if err != nil {
		a.logger.Error("api_resolve_failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return nil, "", false
	}
	var viewer *store.User
	if p != nil {
		viewer = p.user
	}
	level, err := a.deps.Perms.ForRepository(r.Context(), viewer, repo)
	if err != nil {
		a.logger.Error("api_perm_failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return nil, "", false
	}
	if level < permissions.Read {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, "", false
	}
	diskPath, err := a.deps.Repos.DiskPath(repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return nil, "", false
	}
	return repo, diskPath, true
}

// ---------------------------------------------------------------- encoding

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

type userJSON struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt string `json:"created_at"`
}

type repoJSON struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	CloneURL      string `json:"clone_url"`
}

func (a *API) repoToJSON(r *http.Request, repo *store.Repository) repoJSON {
	return repoJSON{
		Owner:         repo.Owner,
		Name:          repo.Name,
		Description:   repo.Description,
		Visibility:    repo.Visibility,
		DefaultBranch: repo.DefaultBranch,
		Archived:      repo.IsArchived,
		CreatedAt:     repo.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     repo.UpdatedAt.UTC().Format(time.RFC3339),
		CloneURL:      "http://" + r.Host + "/" + repo.Owner + "/" + repo.Name + ".git",
	}
}

func pagination(r *http.Request) (page, perPage int) {
	page, perPage = 1, 30
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n >= 1 && n <= 10000 {
		page = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && n >= 1 && n <= 100 {
		perPage = n
	}
	return page, perPage
}

// ------------------------------------------------------------------ users

func (a *API) handleCurrentUser(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": userJSON{
			Username:  p.user.Username,
			Email:     p.user.Email,
			IsAdmin:   p.user.IsAdmin,
			CreatedAt: p.user.CreatedAt.UTC().Format(time.RFC3339),
		},
	})
}

// ------------------------------------------------------------------- repos

func (a *API) handleUserRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	repos, err := a.deps.Store.RepositoriesByOwner(r.Context(), p.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	a.writeRepoList(w, r, repos)
}

func (a *API) handleListRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	var repos []*store.Repository
	if auth.HasScope(p.token.Scopes, auth.ScopeAdmin) {
		repos, ok = a.allRepos(w, r)
	} else {
		// Without the admin scope the listing is the caller's own.
		var err error
		repos, err = a.deps.Store.RepositoriesByOwner(r.Context(), p.user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
	}
	a.writeRepoList(w, r, repos)
}

func (a *API) allRepos(w http.ResponseWriter, r *http.Request) ([]*store.Repository, bool) {
	repos, err := a.deps.Store.AllRepositories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return nil, false
	}
	return repos, true
}

func (a *API) writeRepoList(w http.ResponseWriter, r *http.Request, repos []*store.Repository) {
	page, perPage := pagination(r)
	start := (page - 1) * perPage
	if start > len(repos) {
		start = len(repos)
	}
	end := start + perPage
	if end > len(repos) {
		end = len(repos)
	}
	pageRepos := repos[start:end]

	data := make([]repoJSON, 0, len(pageRepos))
	for _, repo := range pageRepos {
		data = append(data, a.repoToJSON(r, repo))
	}
	body := map[string]any{
		"data":     data,
		"page":     page,
		"per_page": perPage,
	}
	if end < len(repos) {
		body["next_page"] = page + 1
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *API) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoWrite)
	if !ok {
		return
	}
	var req struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Visibility    string `json:"visibility"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	repo, err := a.deps.Repos.Create(r.Context(), p.user, req.Name, reposervice.CreateOptions{
		Description:   req.Description,
		Visibility:    req.Visibility,
		DefaultBranch: req.DefaultBranch,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	_ = a.deps.Store.Audit(r.Context(), &p.user.ID, "repo.created", repo.Owner+"/"+repo.Name, "api")
	writeJSON(w, http.StatusCreated, map[string]any{"repository": a.repoToJSON(r, repo)})
}

func (a *API) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	repo, _, ok := a.resolveRepo(w, r, p)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": a.repoToJSON(r, repo)})
}

func (a *API) handleUpdateRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoWrite)
	if !ok {
		return
	}
	repo, diskPath, ok := a.resolveRepo(w, r, p)
	if !ok {
		return
	}
	level, err := a.deps.Perms.ForRepository(r.Context(), p.user, repo)
	if err != nil || level < permissions.Admin {
		writeError(w, http.StatusForbidden, "forbidden", "you do not manage this repository")
		return
	}

	var req struct {
		Description   *string `json:"description"`
		Visibility    *string `json:"visibility"`
		DefaultBranch *string `json:"default_branch"`
		Archived      *bool   `json:"archived"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Visibility != nil {
		if err := store.ValidateVisibility(*req.Visibility); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	if req.DefaultBranch != nil && *req.DefaultBranch != repo.DefaultBranch {
		if err := git.ValidateBranchName(*req.DefaultBranch); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if err := a.deps.Git.SetDefaultBranch(r.Context(), diskPath, *req.DefaultBranch); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "branch does not exist")
			return
		}
	}
	if err := a.deps.Store.UpdateRepository(r.Context(), repo.ID,
		nil, req.Description, req.Visibility, req.DefaultBranch, req.Archived); err != nil {
		a.logger.Error("api_update_failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	fresh, err := a.deps.Repos.Resolve(r.Context(), repo.Owner, repo.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	_ = a.deps.Store.Audit(r.Context(), &p.user.ID, "repo.updated", fresh.Owner+"/"+fresh.Name, "api")
	writeJSON(w, http.StatusOK, map[string]any{"repository": a.repoToJSON(r, fresh)})
}

func (a *API) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoWrite)
	if !ok {
		return
	}
	repo, _, ok := a.resolveRepo(w, r, p)
	if !ok {
		return
	}
	level, err := a.deps.Perms.ForRepository(r.Context(), p.user, repo)
	if err != nil || level < permissions.Admin {
		writeError(w, http.StatusForbidden, "forbidden", "you do not manage this repository")
		return
	}
	target := repo.Owner + "/" + repo.Name
	if err := a.deps.Repos.Delete(r.Context(), repo); err != nil {
		a.logger.Error("api_delete_failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	_ = a.deps.Store.Audit(r.Context(), &p.user.ID, "repo.deleted", target, "api")
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------- refs+commits

type refJSON struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Subject string `json:"subject,omitempty"`
	Author  string `json:"author,omitempty"`
	When    string `json:"when,omitempty"`
}

func (a *API) handleBranches(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	_, diskPath, ok := a.resolveRepo(w, r, p)
	if !ok {
		return
	}
	branches, err := a.deps.Git.ListBranches(r.Context(), diskPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no refs")
		return
	}
	data := make([]refJSON, 0, len(branches))
	for _, b := range branches {
		data = append(data, refJSON{
			Name: b.Short, SHA: b.Target, Subject: b.Subject,
			Author: b.Author, When: b.When.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (a *API) handleTags(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	_, diskPath, ok := a.resolveRepo(w, r, p)
	if !ok {
		return
	}
	tags, err := a.deps.Git.ListTags(r.Context(), diskPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no refs")
		return
	}
	data := make([]refJSON, 0, len(tags))
	for _, t := range tags {
		data = append(data, refJSON{
			Name: t.Short, SHA: t.Target, Subject: t.Subject,
			Author: t.Author, When: t.When.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type commitJSON struct {
	SHA       string   `json:"sha"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body,omitempty"`
	Parents   []string `json:"parents"`
	Author    *who     `json:"author"`
	Committer *who     `json:"committer"`
}

type who struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Time  string `json:"time"`
}

func (a *API) handleCommits(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireScope(w, r, auth.ScopeRepoRead)
	if !ok {
		return
	}
	repo, diskPath, ok := a.resolveRepo(w, r, p)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	full := "refs/heads/" + ref
	if strings.HasPrefix(ref, "refs/") || ref == "HEAD" || len(ref) == 40 || len(ref) == 64 {
		full = ref
	}
	page, perPage := pagination(r)

	commits, err := a.deps.Git.Log(r.Context(), diskPath, full, (page-1)*perPage, perPage+1)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "ref not found")
		return
	}
	hasMore := false
	if len(commits) > perPage {
		commits = commits[:perPage]
		hasMore = true
	}
	data := make([]commitJSON, 0, len(commits))
	for _, c := range commits {
		data = append(data, commitJSON{
			SHA:     c.Hash,
			Subject: c.Subject,
			Body:    c.Body,
			Parents: c.Parents,
			Author: &who{Name: c.AuthorName, Email: c.AuthorEmail,
				Time: c.AuthorTime.Format(time.RFC3339)},
			Committer: &who{Name: c.CommitterName, Email: c.CommitterEmail,
				Time: c.CommitTime.Format(time.RFC3339)},
		})
	}
	body := map[string]any{"data": data, "page": page, "per_page": perPage}
	if hasMore {
		body["next_page"] = page + 1
	}
	writeJSON(w, http.StatusOK, body)
}

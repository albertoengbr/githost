package web

import (
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// repoContext is the outcome of resolving a repository page request.
type repoContext struct {
	repo      *store.Repository
	view      repoView
	su        *store.SessionUser // nil for anonymous
	level     permissions.Level
	canManage bool
	diskPath  string
} // resolveRepo resolves owner/name with the same privacy policy as the Git
// transports: unauthorized viewers get a plain 404 (existence is never
// disclosed).
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (*repoContext, bool) {
	_, su, _ := h.pageData(r)
	owner := store.NormalizeUsername(r.PathValue("owner"))
	name := strings.TrimSuffix(r.PathValue("repo"), ".git")

	repo, err := h.deps.Repos.Resolve(r.Context(), owner, name)
	if errors.Is(err, reposervice.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		h.logger.Error("repo_resolve_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}

	var viewer *store.User
	if su != nil {
		viewer = &su.User
	}
	level, err := h.deps.Perms.ForRepository(r.Context(), viewer, repo)
	if err != nil {
		h.logger.Error("perm_check_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if level < permissions.Read {
		http.NotFound(w, r)
		return nil, false
	}
	diskPath, err := h.deps.Repos.DiskPath(repo)
	if err != nil {
		h.logger.Error("repo_path_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return &repoContext{
		repo:      repo,
		view:      toRepoView(repo),
		su:        su,
		level:     level,
		canManage: level >= permissions.Admin,
		diskPath:  diskPath,
	}, true
}

// pageRef returns the validated full ref for a page request.
func (h *Handler) pageRef(r *http.Request, rc *repoContext) (string, error) {
	ref := h.repoRef(r, rc.repo)
	full := fullRef(ref)
	// Reuse the executor's revision validator (no extra process needed).
	return full, nil
}

// commitHead renders a repo page with the "repo head" content: latest
// commit, tree listing and README preview.
func (h *Handler) repoHome(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	data := struct {
		pageData
		Repo       repoView
		CanManage  bool
		HasCommits bool
		Latest     *git.Commit
		Entries    []git.TreeEntry
		Path       string
		Ref        string
		Readme     string
		ReadmeName string
		CloneHTTP  string
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage, Ref: rc.repo.DefaultBranch}

	data.CloneHTTP = "http://" + r.Host + "/" + rc.repo.Owner + "/" + rc.repo.Name + ".git"

	latest, err := h.deps.Git.Log(ctx, rc.diskPath, "refs/heads/"+rc.repo.DefaultBranch, 0, 1)
	if err != nil || len(latest) == 0 {
		// Empty repository (or default branch not born yet).
		h.render(w, http.StatusOK, "repo_home.tmpl", data)
		return
	}
	data.HasCommits = true
	data.Latest = &latest[0]
	data.Ref = rc.repo.DefaultBranch

	entries, err := h.deps.Git.LsTree(ctx, rc.diskPath, "refs/heads/"+rc.repo.DefaultBranch, "")
	if err == nil {
		data.Entries = entries
		// README preview: README.md first, then any README*, ≤ 256 KiB.
		for _, want := range []string{"README.md", "README.txt", "README"} {
			for _, entry := range entries {
				if entry.Type == "blob" && entry.Name == want {
					if content, err := h.deps.Git.CatFileBlob(ctx, rc.diskPath,
						"refs/heads/"+rc.repo.DefaultBranch, entry.Name, 256<<10); err == nil {
						data.Readme = string(content)
						data.ReadmeName = entry.Name
					}
					break
				}
			}
			if data.Readme != "" {
				break
			}
		}
		// Case-insensitive fallback.
		if data.Readme == "" {
			for _, entry := range entries {
				if entry.Type == "blob" && strings.HasPrefix(strings.ToUpper(entry.Name), "README") {
					if content, err := h.deps.Git.CatFileBlob(ctx, rc.diskPath,
						"refs/heads/"+rc.repo.DefaultBranch, entry.Name, 256<<10); err == nil {
						data.Readme = string(content)
						data.ReadmeName = entry.Name
					}
					break
				}
			}
		}
	}
	h.render(w, http.StatusOK, "repo_home.tmpl", data)
}

func (h *Handler) repoTree(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	refName, err := h.pageRef(r, rc)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	treePath := r.PathValue("path")

	data := struct {
		pageData
		Repo      repoView
		CanManage bool
		Entries   []git.TreeEntry
		Path      string
		Ref       string
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage, Path: treePath, Ref: r.URL.Query().Get("ref")}

	if treePath == "" {
		http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name, http.StatusSeeOther)
		return
	}
	entries, err := h.deps.Git.LsTree(r.Context(), rc.diskPath, refName, treePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data.Entries = entries
	h.render(w, http.StatusOK, "repo_tree.tmpl", data)
}

func (h *Handler) repoBlob(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	refName, err := h.pageRef(r, rc)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	blobPath := r.PathValue("path")

	data := struct {
		pageData
		Repo      repoView
		CanManage bool
		Path      string
		Ref       string
		Content   string
		Size      int64
		Binary    bool
		Truncated bool
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage, Path: blobPath, Ref: r.URL.Query().Get("ref")}

	content, err := h.deps.Git.CatFileBlob(r.Context(), rc.diskPath, refName, blobPath, 512<<10)
	if err != nil {
		var tooLarge *git.ErrBlobTooLarge
		if errors.As(err, &tooLarge) {
			data.Truncated = true
			data.Size = tooLarge.Size
			h.render(w, http.StatusOK, "repo_blob.tmpl", data)
			return
		}
		http.NotFound(w, r)
		return
	}
	data.Size = int64(len(content))
	data.Binary = git.IsBinary(content)
	if !data.Binary {
		data.Content = string(content)
	}
	h.render(w, http.StatusOK, "repo_blob.tmpl", data)
}

func (h *Handler) repoRaw(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	refName, err := h.pageRef(r, rc)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	blobPath := r.PathValue("path")

	// Size probe first: refuse absurd requests before buffering.
	if _, err := h.deps.Git.CatFileBlob(r.Context(), rc.diskPath, refName, blobPath, 64<<20); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		`inline; filename="`+path.Base(blobPath)+`"`)
	w.Header().Set("Cache-Control", "private, max-age=60")
	// Stream the blob straight to the client through the executor.
	if _, err := h.deps.Git.Run(r.Context(), git.Options{
		Args:   []string{"cat-file", "blob", refName + ":" + blobPath},
		Dir:    rc.diskPath,
		Stdout: w,
	}); err != nil {
		h.logger.Error("raw_blob_failed", "err", err)
	}
}

func (h *Handler) repoCommits(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	refName, err := h.pageRef(r, rc)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	const perPage = 30
	page := 1
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n >= 1 && n <= 1000 {
		page = n
	}

	commits, err := h.deps.Git.Log(r.Context(), rc.diskPath, refName, (page-1)*perPage, perPage+1)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hasMore := false
	if len(commits) > perPage {
		commits = commits[:perPage]
		hasMore = true
	}
	display := r.URL.Query().Get("ref")
	if display == "" {
		display = rc.repo.DefaultBranch
	}
	data := struct {
		pageData
		Repo      repoView
		CanManage bool
		Commits   []git.Commit
		Ref       string
		Page      int
		HasMore   bool
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage,
		Commits: commits, Ref: display, Page: page, HasMore: hasMore}
	h.render(w, http.StatusOK, "repo_commits.tmpl", data)
}

func (h *Handler) repoCommit(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	sha := r.PathValue("sha")
	commit, err := h.deps.Git.Commit(r.Context(), rc.diskPath, sha)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	patch, _ := h.deps.Git.CommitPatch(r.Context(), rc.diskPath, sha)

	data := struct {
		pageData
		Repo      repoView
		CanManage bool
		Commit    *git.Commit
		Patch     string
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage, Commit: commit, Patch: patch}
	h.render(w, http.StatusOK, "repo_commit.tmpl", data)
}

func (h *Handler) repoBranches(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	branches, err := h.deps.Git.ListBranches(r.Context(), rc.diskPath)
	if err != nil {
		branches = nil
	}
	data := struct {
		pageData
		Repo      repoView
		CanManage bool
		Branches  []git.Ref
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage, Branches: branches}
	h.render(w, http.StatusOK, "repo_branches.tmpl", data)
}

func (h *Handler) repoTags(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	tags, err := h.deps.Git.ListTags(r.Context(), rc.diskPath)
	if err != nil {
		tags = nil
	}
	data := struct {
		pageData
		Repo      repoView
		CanManage bool
		Tags      []git.Ref
	}{pageData: pd, Repo: rc.view, CanManage: rc.canManage, Tags: tags}
	h.render(w, http.StatusOK, "repo_tags.tmpl", data)
}

package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"githost.dev/githost/internal/events"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/markdown"
	"githost.dev/githost/internal/store"
)

// issueView is the template model for one issue.
type issueView struct {
	Number       int64
	Title        string
	BodyHTML     template.HTML
	State        string
	Author       string
	Assignee     string
	CreatedAt    string
	CommentCount int
	Labels       []store.Label
}

// issueCommentView for the comment thread.
type issueCommentView struct {
	Author    string
	BodyHTML  template.HTML
	CreatedAt string
}

func issueToView(it *store.Issue) issueView {
	v := issueView{
		Number:       it.Number,
		Title:        it.Title,
		State:        it.State,
		Author:       it.Author,
		Assignee:     it.Assignee,
		CommentCount: it.CommentCount,
		Labels:       it.Labels,
	}
	v.CreatedAt = it.CreatedAt.Format("2006-01-02 15:04")
	if html, err := markdown.Render(it.BodyMD); err == nil {
		v.BodyHTML = html
	}
	return v
}

func commentToView(c *store.IssueComment) issueCommentView {
	v := issueCommentView{Author: c.Author, CreatedAt: c.CreatedAt.Format("2006-01-02 15:04")}
	if html, err := markdown.Render(c.BodyMD); err == nil {
		v.BodyHTML = html
	}
	return v
}

// Issue list page.
func (h *Handler) issueList(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	issues, err := h.deps.Store.Issues(r.Context(), rc.repo.ID, state)
	if err != nil {
		h.logger.Error("issue_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var views []issueView
	for _, it := range issues {
		views = append(views, issueToView(it))
	}
	data := struct {
		pageData
		Repo   repoView
		State  string
		Issues []issueView
	}{pageData: pd, Repo: rc.view, State: state, Issues: views}
	h.render(w, http.StatusOK, "issue_list.tmpl", data)
}

// Issue creation form.
func (h *Handler) issueNew(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	_ = su
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	data := struct {
		pageData
		Repo      repoView
		FormError string
	}{pageData: pd, Repo: rc.view}
	h.render(w, http.StatusOK, "issue_new.tmpl", data)
}

func (h *Handler) issueNewSubmit(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Redirect(w, r, r.URL.Path+"?msg=err-bad-csrf", http.StatusSeeOther)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	issue, err := h.deps.Store.CreateIssue(r.Context(), rc.repo.ID, su.User.ID,
		strings.TrimSpace(r.PostFormValue("title")),
		strings.TrimSpace(r.PostFormValue("body")))
	if err != nil {
		pd.Flash = err.Error()
		pd.FlashKind = "err"
		data := struct {
			pageData
			Repo      repoView
			FormError string
		}{pageData: pd, Repo: rc.view, FormError: err.Error()}
		h.render(w, http.StatusBadRequest, "issue_new.tmpl", data)
		return
	}

	// F5: emit issue.opened event.
	if h.deps.Events != nil {
		h.deps.Events.Emit(r.Context(), events.Event{
			Kind:       events.KindIssueOpened,
			Repository: rc.repo.Owner + "/" + rc.repo.Name,
			Actor:      su.User.Username,
			Detail:     `{"number":` + strconv.FormatInt(issue.Number, 10) + `,"title":` + strconv.Quote(issue.Title) + `}`,
		})
	}

	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/issues/"+strconv.FormatInt(issue.Number, 10),
		http.StatusSeeOther)
}

// Issue detail with comments and history.
func (h *Handler) issueDetail(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number < 1 {
		http.NotFound(w, r)
		return
	}
	issue, err := h.deps.Store.IssueByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	comments, _ := h.deps.Store.IssueComments(r.Context(), issue.ID)
	events, _ := h.deps.Store.IssueEvents(r.Context(), issue.ID)

	var commentViews []issueCommentView
	for _, c := range comments {
		commentViews = append(commentViews, commentToView(c))
	}
	type eventView struct {
		Actor, Kind, Detail, When string
	}
	var eventViews []eventView
	for _, e := range events {
		eventViews = append(eventViews, eventView{
			Actor: e.Actor, Kind: e.Kind, Detail: e.Detail,
			When: e.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	// Labels for the toggle form.
	repoLabels, _ := h.deps.Store.Labels(r.Context(), rc.repo.ID)

	data := struct {
		pageData
		Repo        repoView
		Issue       issueView
		Comments    []issueCommentView
		Events      []eventView
		Labels      []store.Label
		IssueLabels map[int64]bool
	}{pageData: pd, Repo: rc.view, Issue: issueToView(issue),
		Comments: commentViews, Events: eventViews, Labels: repoLabels,
		IssueLabels: map[int64]bool{}}
	for _, l := range issue.Labels {
		data.IssueLabels[l.ID] = true
	}
	h.render(w, http.StatusOK, "issue_detail.tmpl", data)
}

func (h *Handler) issueComment(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	_ = pd
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	issue, err := h.deps.Store.IssueByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.deps.Store.AddIssueComment(r.Context(), issue.ID, su.User.ID,
		strings.TrimSpace(r.PostFormValue("body"))); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

func (h *Handler) issueSetState(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	issue, err := h.deps.Store.IssueByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state := "closed"
	if r.PostFormValue("action") == "reopen" {
		state = "open"
	}
	if err := h.deps.Store.SetIssueState(r.Context(), issue.ID, su.User.ID, state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/issues/"+strconv.FormatInt(number, 10), http.StatusSeeOther)
}

func (h *Handler) issueToggleLabel(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	labelID, _ := strconv.ParseInt(r.PostFormValue("label_id"), 10, 64)
	issue, err := h.deps.Store.IssueByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.PostFormValue("action") == "remove" {
		_ = h.deps.Store.RemoveIssueLabel(r.Context(), issue.ID, labelID, su.User.ID)
	} else {
		_ = h.deps.Store.AddIssueLabel(r.Context(), issue.ID, labelID, su.User.ID)
	}
	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/issues/"+strconv.FormatInt(number, 10), http.StatusSeeOther)
}

// ------------------------------------------------------------------- PRs

type pullView struct {
	Number      int64
	Title       string
	BodyHTML    template.HTML
	State       string
	Base, Head  string
	Author      string
	CreatedAt   string
	MergeCommit string
}

func pullToView(p *store.PullRequest) pullView {
	v := pullView{
		Number: p.Number, Title: p.Title, State: p.State,
		Base: p.BaseBranch, Head: p.HeadBranch, Author: p.Author,
		MergeCommit: p.MergeCommit,
	}
	v.CreatedAt = p.CreatedAt.Format("2006-01-02 15:04")
	if html, err := markdown.Render(p.BodyMD); err == nil {
		v.BodyHTML = html
	}
	return v
}

func (h *Handler) pullList(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	pulls, err := h.deps.Store.PullRequests(r.Context(), rc.repo.ID, state)
	if err != nil {
		h.logger.Error("pull_list_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var views []pullView
	for _, p := range pulls {
		views = append(views, pullToView(p))
	}
	data := struct {
		pageData
		Repo  repoView
		State string
		Pulls []pullView
	}{pageData: pd, Repo: rc.view, State: state, Pulls: views}
	h.render(w, http.StatusOK, "pull_list.tmpl", data)
}

func (h *Handler) pullNew(w http.ResponseWriter, r *http.Request) {
	pd, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	branches, _ := h.deps.Git.ListBranches(r.Context(), rc.diskPath)
	data := struct {
		pageData
		Repo      repoView
		Branches  []git.Ref
		FormError string
	}{pageData: pd, Repo: rc.view, Branches: branches}
	h.render(w, http.StatusOK, "pull_new.tmpl", data)
}

func (h *Handler) pullNewSubmit(w http.ResponseWriter, r *http.Request) {
	pd, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if h.deps.Pulls == nil {
		http.Error(w, "pull requests not configured", http.StatusServiceUnavailable)
		return
	}
	pr, err := h.deps.Pulls.Create(r.Context(), rc.repo, &su.User,
		strings.TrimSpace(r.PostFormValue("title")),
		strings.TrimSpace(r.PostFormValue("body")),
		r.PostFormValue("base"),
		r.PostFormValue("head"))
	if err != nil {
		pd.Flash = err.Error()
		pd.FlashKind = "err"
		branches, _ := h.deps.Git.ListBranches(r.Context(), rc.diskPath)
		data := struct {
			pageData
			Repo      repoView
			Branches  []git.Ref
			FormError string
		}{pageData: pd, Repo: rc.view, Branches: branches, FormError: err.Error()}
		h.render(w, http.StatusBadRequest, "pull_new.tmpl", data)
		return
	}

	// F5: emit pr.opened event.
	if h.deps.Events != nil {
		h.deps.Events.Emit(r.Context(), events.Event{
			Kind:       events.KindPROpened,
			Repository: rc.repo.Owner + "/" + rc.repo.Name,
			Actor:      su.User.Username,
			Detail:     `{"number":` + strconv.FormatInt(pr.Number, 10) + `,"base":"` + pr.BaseBranch + `","head":"` + pr.HeadBranch + `"}`,
		})
	}

	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/pulls/"+strconv.FormatInt(pr.Number, 10),
		http.StatusSeeOther)
}

func (h *Handler) pullDetail(w http.ResponseWriter, r *http.Request) {
	pd, _, _ := h.pageData(r)
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number < 1 {
		http.NotFound(w, r)
		return
	}
	if h.deps.Pulls == nil {
		http.Error(w, "pull requests not configured", http.StatusServiceUnavailable)
		return
	}
	detail, err := h.deps.Pulls.Detail(r.Context(), rc.repo, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var reviews []struct {
		Reviewer, Verdict string
		BodyHTML          template.HTML
		When              string
	}
	for _, rev := range detail.Reviews {
		var bodyHTML template.HTML
		if html, err := markdown.Render(rev.BodyMD); err == nil {
			bodyHTML = html
		}
		reviews = append(reviews, struct {
			Reviewer, Verdict string
			BodyHTML          template.HTML
			When              string
		}{rev.Reviewer, rev.Verdict, bodyHTML, rev.CreatedAt.Format("2006-01-02 15:04")})
	}
	var comments []issueCommentView
	for _, c := range detail.Comments {
		comments = append(comments, commentToView(c))
	}

	var conflicts []string
	for _, c := range detail.Conflicts {
		conflicts = append(conflicts, c)
	}
	var rules []struct {
		Pattern         string
		RequireApproval bool
	}
	for _, rule := range detail.Rules {
		rules = append(rules, struct {
			Pattern         string
			RequireApproval bool
		}{rule.Pattern, rule.RequireApproval})
	}

	data := struct {
		pageData
		Repo    repoView
		PR      pullView
		Commits []git.Commit
		Diff    string
		Reviews []struct {
			Reviewer, Verdict string
			BodyHTML          template.HTML
			When              string
		}
		Comments  []issueCommentView
		Conflicts []string
		Rules     []struct {
			Pattern         string
			RequireApproval bool
		}
		Approvals, Rejections int
	}{pageData: pd, Repo: rc.view, PR: pullToView(detail.PR),
		Commits: detail.Commits, Diff: detail.Diff,
		Reviews: reviews, Comments: comments,
		Conflicts: conflicts, Rules: rules,
		Approvals: detail.Approvals, Rejections: detail.Rejections}
	h.render(w, http.StatusOK, "pull_detail.tmpl", data)
}

func (h *Handler) pullReview(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if h.deps.Pulls == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	pr, err := h.deps.Store.PullRequestByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	verdict := r.PostFormValue("verdict")
	body := strings.TrimSpace(r.PostFormValue("body"))
	if _, err := h.deps.Pulls.Review(r.Context(), pr, &su.User, verdict, body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/pulls/"+strconv.FormatInt(number, 10), http.StatusSeeOther)
}

func (h *Handler) pullComment(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if h.deps.Pulls == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	pr, err := h.deps.Store.PullRequestByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.deps.Pulls.Comment(r.Context(), pr, &su.User,
		strings.TrimSpace(r.PostFormValue("body"))); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/pulls/"+strconv.FormatInt(number, 10), http.StatusSeeOther)
}

func (h *Handler) pullMerge(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if h.deps.Pulls == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	pr, err := h.deps.Store.PullRequestByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	strategy := r.PostFormValue("strategy")
	if _, err := h.deps.Pulls.Merge(r.Context(), rc.repo, pr, &su.User, strategy); err != nil {
		pd, _, _ := h.pageData(r)
		pd.Flash = err.Error()
		pd.FlashKind = "err"
		http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/pulls/"+strconv.FormatInt(number, 10)+"?msg=err-merge", http.StatusSeeOther)
		return
	}

	// F5: emit pr.merged event.
	if h.deps.Events != nil {
		h.deps.Events.Emit(r.Context(), events.Event{
			Kind:       events.KindPRMerged,
			Repository: rc.repo.Owner + "/" + rc.repo.Name,
			Actor:      su.User.Username,
			Detail:     `{"number":` + strconv.FormatInt(number, 10) + `,"strategy":"` + strategy + `"}`,
		})
	}

	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/pulls/"+strconv.FormatInt(number, 10), http.StatusSeeOther)
}

func (h *Handler) pullSetState(w http.ResponseWriter, r *http.Request) {
	_, su, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.verifyCSRF(r, su.Session.ID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if h.deps.Pulls == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	pr, err := h.deps.Store.PullRequestByNumber(r.Context(), rc.repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state := "closed"
	if r.PostFormValue("action") == "reopen" {
		state = "open"
	}
	if err := h.deps.Pulls.SetState(r.Context(), pr, state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/"+rc.repo.Owner+"/"+rc.repo.Name+"/pulls/"+strconv.FormatInt(number, 10), http.StatusSeeOther)
}

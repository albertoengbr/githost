// Package prservice orchestrates pull request workflows: creation,
// review and merge — including branch protection enforcement (the
// charter's requirement: "Garantir que um Pull Request não seja
// integrado quando houver violações das políticas de proteção").
package prservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

var (
	// ErrNotFound mirrors reposervice's ErrNotFound.
	ErrNotFound = errors.New("pull request not found")

	// ErrBranchProtection means a protection rule blocks the merge.
	ErrBranchProtection = errors.New("branch protection rule blocks this merge")

	// ErrAlreadyMerged means the PR is not in the open state.
	ErrAlreadyMerged = errors.New("pull request is not open")

	// ErrMergeConflict passes through the git error.
	ErrMergeConflict = errors.New("merge conflict")
)

// Service wires store, repository service and git executor.
type Service struct {
	store *store.Store
	repos *reposervice.Service
	git   *git.Executor
}

// New builds the PR service.
func New(st *store.Store, repos *reposervice.Service, executor *git.Executor) *Service {
	return &Service{store: st, repos: repos, git: executor}
}

// Create opens a PR after verifying both branches exist in the repo.
func (s *Service) Create(ctx context.Context, repo *store.Repository, author *store.User,
	title, body, baseBranch, headBranch string) (*store.PullRequest, error) {
	if err := git.ValidateBranchName(baseBranch); err != nil {
		return nil, fmt.Errorf("base branch: %w", err)
	}
	if err := git.ValidateBranchName(headBranch); err != nil {
		return nil, fmt.Errorf("head branch: %w", err)
	}
	diskPath, err := s.repos.DiskPath(repo)
	if err != nil {
		return nil, err
	}
	// Both branches must exist.
	for _, branch := range []string{baseBranch, headBranch} {
		if _, err := s.git.RevParseSHA(ctx, diskPath, "refs/heads/"+branch); err != nil {
			return nil, fmt.Errorf("branch %q not found", branch)
		}
	}
	// There must be at least one commit to merge.
	commits, err := s.git.CommitsBetween(ctx, diskPath, "refs/heads/"+baseBranch, "refs/heads/"+headBranch, 1)
	if err != nil || len(commits) == 0 {
		return nil, fmt.Errorf("no commits between %s and %s", baseBranch, headBranch)
	}
	return s.store.CreatePullRequest(ctx, repo.ID, author.ID, title, body, baseBranch, headBranch)
}

// Detail loads a PR plus its review state.
type Detail struct {
	PR         *store.PullRequest
	Reviews    []*store.PullRequestReview
	Comments   []*store.IssueComment
	Commits    []git.Commit
	Diff       string
	Conflicts  []string // non-empty when the merge does not apply cleanly
	Approvals  int
	Rejections int
	Rules      []*store.BranchRule // rules matching the base branch
}

func (s *Service) Detail(ctx context.Context, repo *store.Repository, number int64) (*Detail, error) {
	pr, err := s.store.PullRequestByNumber(ctx, repo.ID, number)
	if err != nil {
		return nil, ErrNotFound
	}
	diskPath, err := s.repos.DiskPath(repo)
	if err != nil {
		return nil, err
	}
	detail := &Detail{PR: pr}
	detail.Reviews, _ = s.store.PullRequestReviews(ctx, pr.ID)
	detail.Comments, _ = s.store.PullRequestComments(ctx, pr.ID)
	detail.Approvals, detail.Rejections, _ = s.store.ApprovalState(ctx, pr.ID)

	if pr.State == "open" {
		baseRef := "refs/heads/" + pr.BaseBranch
		headRef := "refs/heads/" + pr.HeadBranch
		detail.Commits, _ = s.git.CommitsBetween(ctx, diskPath, baseRef, headRef, 100)
		detail.Diff, _ = s.git.DiffBetween(ctx, diskPath, baseRef, headRef)
		if status, err := s.git.CheckMerge(ctx, diskPath, baseRef, headRef); err == nil {
			detail.Conflicts = status.Conflicts
		}
	}

	// Protection rules matching the base branch.
	rules, err := s.store.BranchRules(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if MatchBranchPattern(rule.Pattern, pr.BaseBranch) {
			detail.Rules = append(detail.Rules, rule)
		}
	}
	return detail, nil
}

// Review records a verdict.
func (s *Service) Review(ctx context.Context, pr *store.PullRequest, reviewer *store.User,
	verdict, body string) (*store.PullRequestReview, error) {
	if pr.State != "open" {
		return nil, ErrAlreadyMerged
	}
	return s.store.AddPullRequestReview(ctx, pr.ID, reviewer.ID, verdict, body)
}

// Comment adds a comment.
func (s *Service) Comment(ctx context.Context, pr *store.PullRequest, author *store.User, body string) (*store.IssueComment, error) {
	return s.store.AddPullRequestComment(ctx, pr.ID, author.ID, body)
}

// Close closes (or reopens) a PR.
func (s *Service) SetState(ctx context.Context, pr *store.PullRequest, state string) error {
	return s.store.SetPullRequestState(ctx, pr.ID, state)
}

// Merge executes the PR merge after enforcing protection rules.
// This is the single entry point: the web UI, the CLI and the API all
// go through it (defense in depth against bypassing a single surface).
func (s *Service) Merge(ctx context.Context, repo *store.Repository, pr *store.PullRequest,
	merger *store.User, strategy string) (string, error) {
	if pr.State != "open" {
		return "", ErrAlreadyMerged
	}
	if !git.ValidMergeStrategy(strategy) {
		return "", fmt.Errorf("merge strategy %q is not supported (use merge-commit, squash or fast-forward-only)", strategy)
	}
	diskPath, err := s.repos.DiskPath(repo)
	if err != nil {
		return "", err
	}

	// ---- Branch protection enforcement ----
	rules, err := s.store.BranchRules(ctx, repo.ID)
	if err != nil {
		return "", err
	}
	var matched []*store.BranchRule
	for _, rule := range rules {
		if MatchBranchPattern(rule.Pattern, pr.BaseBranch) {
			matched = append(matched, rule)
		}
	}
	for _, rule := range matched {
		if rule.RequireApproval {
			approvals, rejections, err := s.store.ApprovalState(ctx, pr.ID)
			if err != nil {
				return "", err
			}
			if rejections > 0 {
				return "", fmt.Errorf("%w: %q requires approval but has %d rejection(s)",
					ErrBranchProtection, rule.Pattern, rejections)
			}
			if approvals == 0 {
				return "", fmt.Errorf("%w: %q requires at least one approval before merge",
					ErrBranchProtection, rule.Pattern)
			}
		}
	}

	mergeSHA, err := s.git.MergePR(ctx, diskPath, pr.BaseBranch, pr.HeadBranch, git.MergeOptions{
		Strategy:       git.MergeStrategy(strategy),
		CommitterName:  "GitHost",
		CommitterEmail: "githost@localhost",
		Message:        fmt.Sprintf("Merge pull request #%d: %s", pr.Number, pr.Title),
	})
	if err != nil {
		var conflict *git.ErrMergeConflict
		if errors.As(err, &conflict) {
			return "", fmt.Errorf("%w: %s", ErrMergeConflict, conflict.Error())
		}
		return "", err
	}

	// Record the merge in the PR row. The CAS update in MarkMerged
	// (state='open' guard) protects against concurrent merges.
	if err := s.store.MarkMerged(ctx, pr.ID, merger.ID, mergeSHA); err != nil {
		return "", fmt.Errorf("record merge: %w", err)
	}
	_ = s.store.Audit(ctx, &merger.ID, "pr.merged",
		repo.Owner+"/"+repo.Name+"#"+fmt.Sprintf("%d", pr.Number), strategy)
	return mergeSHA, nil
}

// MatchBranchPattern reports whether a branch name matches a rule
// pattern. Supported: exact names, "*" within a segment, "**" across
// segments. Our own glob ("/" separator — path.Match's separator is
// OS-dependent).
func MatchBranchPattern(pattern, branch string) bool {
	if pattern == branch {
		return true
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(branch, "/"))
}

// matchSegments walks pattern and branch segments: "**" consumes any
// number of branch segments; "*" within a segment matches that segment.
func matchSegments(pat, str []string) bool {
	if len(pat) == 0 {
		return len(str) == 0
	}
	if pat[0] == "**" {
		// "**" matches zero or more segments.
		for skip := 0; skip <= len(str); skip++ {
			if matchSegments(pat[1:], str[skip:]) {
				return true
			}
		}
		return false
	}
	if len(str) == 0 {
		return false
	}
	if !matchSegment(pat[0], str[0]) {
		return false
	}
	return matchSegments(pat[1:], str[1:])
}

// matchSegment matches one segment where "*" does not cross "/".
func matchSegment(pat, str string) bool {
	if pat == "*" {
		return true
	}
	for len(pat) > 0 {
		if pat[0] == '*' {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(str); i++ {
				if matchSegment(pat[1:], str[i:]) {
					return true
				}
			}
			return false
		}
		if len(str) == 0 || (pat[0] != '?' && pat[0] != str[0]) {
			return false
		}
		pat, str = pat[1:], str[1:]
	}
	return len(str) == 0
}

package prservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

type fixture struct {
	svc   *Service
	st    *store.Store
	repo  *store.Repository
	dir   string // bare repo path
	alice *store.User
	bob   *store.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()

	db, err := database.Open(ctx, filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	executor, err := git.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "repositories")
	repoStorage, err := storage.NewRepositories(repoDir, executor)
	if err != nil {
		t.Fatal(err)
	}
	reposvc := reposervice.New(st, repoStorage)

	hash, _ := auth.HashPassword("x")
	alice, _ := st.CreateUser(ctx, "alice", "", hash, false)
	bob, _ := st.CreateUser(ctx, "bob", "", hash, false)

	repo, err := reposvc.Create(ctx, alice, "proj", reposervice.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repoDir, repo.UUID+".git")

	// Push main + dev with real git (client-side pusher).
	work := filepath.Join(t.TempDir(), "w")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runWork(t, work, "init", "--initial-branch=main")
	writeWork(t, work, "base.txt", "base\n")
	runWork(t, work, "add", ".")
	runWork(t, work, "commit", "-m", "A")
	runWork(t, work, "checkout", "-b", "dev")
	writeWork(t, work, "dev.txt", "dev change\n")
	runWork(t, work, "add", ".")
	runWork(t, work, "commit", "-m", "B")
	runWork(t, work, "push", filepath.ToSlash(dir), "main", "dev")

	return &fixture{
		svc:   New(st, reposvc, executor),
		st:    st,
		repo:  repo,
		dir:   dir,
		alice: alice,
		bob:   bob,
	}
}

func writeWork(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runWork(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=W", "GIT_AUTHOR_EMAIL=w@t.test",
		"GIT_COMMITTER_NAME=W", "GIT_COMMITTER_EMAIL=w@t.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestPRWorkflowWithProtection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Open a PR.
	pr, err := f.svc.Create(ctx, f.repo, f.alice, "Add dev feature", "desc", "main", "dev")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pr.Number != 1 || pr.State != "open" {
		t.Fatalf("pr = %+v", pr)
	}

	// Detail includes commits, diff and no conflicts.
	detail, err := f.svc.Detail(ctx, f.repo, 1)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(detail.Commits) != 1 || !strings.Contains(detail.Commits[0].Subject, "B") {
		t.Fatalf("commits = %+v", detail.Commits)
	}
	if !strings.Contains(detail.Diff, "dev.txt") {
		t.Fatalf("diff missing dev.txt:\n%s", detail.Diff)
	}
	if len(detail.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", detail.Conflicts)
	}

	// --- Protection: require approval on main ---
	rule, err := f.st.AddBranchRule(ctx, store.BranchRule{
		RepositoryID:    f.repo.ID,
		Pattern:         "main",
		RequireApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Merge without approval → blocked.
	if _, err := f.svc.Merge(ctx, f.repo, detail.PR, f.alice, "merge-commit"); err == nil {
		t.Fatal("merge bypassed require_approval")
	} else if !strings.Contains(err.Error(), "requires at least one approval") {
		t.Fatalf("err = %v", err)
	}

	// Approve from bob, then merge works.
	if _, err := f.svc.Review(ctx, detail.PR, f.bob, store.ReviewApprove, "lgtm"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	mergeSHA, err := f.svc.Merge(ctx, f.repo, detail.PR, f.alice, "merge-commit")
	if err != nil {
		t.Fatalf("Merge with approval: %v", err)
	}
	if mergeSHA == "" {
		t.Fatal("empty merge sha")
	}

	// PR state is now merged; re-merge refused.
	freshPR, _ := f.st.PullRequestByID(ctx, detail.PR.ID)
	if freshPR.State != "merged" || freshPR.MergeCommit != mergeSHA {
		t.Fatalf("pr after merge = %+v", freshPR)
	}
	if _, err := f.svc.Merge(ctx, f.repo, freshPR, f.alice, "merge-commit"); err == ErrAlreadyMerged {
		// expected
	} else if err == nil {
		t.Fatal("double merge accepted")
	}

	// The merge landed on main.
	out, _ := runGitOutput(t, f.dir, "log", "--format=%s", "refs/heads/main")
	if !strings.Contains(out, "Add dev feature") && !strings.Contains(out, "Merge pull request #1") {
		t.Fatalf("main after merge:\n%s", out)
	}

	// Remove the rule.
	if err := f.st.DeleteBranchRule(ctx, f.repo.ID, rule.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPRRejectionBlocksMerge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	pr, err := f.svc.Create(ctx, f.repo, f.alice, "Another feature", "", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.st.AddBranchRule(ctx, store.BranchRule{
		RepositoryID:    f.repo.ID,
		Pattern:         "main",
		RequireApproval: true,
	})
	// Bob rejects.
	if _, err := f.svc.Review(ctx, pr, f.bob, store.ReviewReject, "needs work"); err != nil {
		t.Fatal(err)
	}
	// Alice (the author) cannot approve her own pull request.
	if _, err := f.svc.Review(ctx, pr, f.alice, store.ReviewApprove, "i approve mine"); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("author self-approval: got err %v, want ErrSelfReview", err)
	}
	if _, err := f.svc.Merge(ctx, f.repo, pr, f.alice, "merge-commit"); err == nil {
		t.Fatal("merge accepted with an outstanding rejection")
	}
}

func TestAuthorCannotApproveOwnPR(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	pr, err := f.svc.Create(ctx, f.repo, f.alice, "Solo feature", "", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddBranchRule(ctx, store.BranchRule{
		RepositoryID:    f.repo.ID,
		Pattern:         "main",
		RequireApproval: true,
	}); err != nil {
		t.Fatal(err)
	}
	// The author cannot satisfy RequireApproval on their own PR…
	if _, err := f.svc.Review(ctx, pr, f.alice, store.ReviewApprove, "self lgtm"); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("author self-approval: got err %v, want ErrSelfReview", err)
	}
	if _, err := f.svc.Merge(ctx, f.repo, pr, f.alice, "merge-commit"); !errors.Is(err, ErrBranchProtection) {
		t.Fatalf("merge without any approval: got err %v, want ErrBranchProtection", err)
	}
	// …but a genuine reviewer's approval unblocks it.
	if _, err := f.svc.Review(ctx, pr, f.bob, store.ReviewApprove, "lgtm"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Merge(ctx, f.repo, pr, f.alice, "merge-commit"); err != nil {
		t.Fatalf("merge with bob's approval: %v", err)
	}
}

func TestReviewReplacesPreviousVerdict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	pr, err := f.svc.Create(ctx, f.repo, f.alice, "Flip flop", "", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Review(ctx, pr, f.bob, store.ReviewApprove, "lgtm"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Review(ctx, pr, f.bob, store.ReviewReject, "on second thought"); err != nil {
		t.Fatal(err)
	}
	reviews, err := f.st.PullRequestReviews(ctx, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 {
		t.Fatalf("got %d review rows for one reviewer, want 1 (upsert)", len(reviews))
	}
	if reviews[0].Verdict != store.ReviewReject {
		t.Fatalf("verdict = %q, want the latest verdict %q", reviews[0].Verdict, store.ReviewReject)
	}
	approvals, rejections, err := f.st.ApprovalState(ctx, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approvals != 0 || rejections != 1 {
		t.Fatalf("approval state = (%d, %d), want (0, 1)", approvals, rejections)
	}
}

func TestMatchBranchPattern(t *testing.T) {
	cases := []struct {
		pattern, branch string
		want            bool
	}{
		{"main", "main", true},
		{"main", "dev", false},
		{"release/*", "release/1.0", true},
		{"release/*", "release/1.0/beta", false}, // single * doesn't cross /
		{"feature/**", "feature/one/two", true},
		{"**", "anything/at/all", true},
	}
	for _, tc := range cases {
		if got := MatchBranchPattern(tc.pattern, tc.branch); got != tc.want {
			t.Errorf("MatchBranchPattern(%q, %q) = %v, want %v", tc.pattern, tc.branch, got, tc.want)
		}
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

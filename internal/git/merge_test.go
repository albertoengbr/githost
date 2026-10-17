package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mergeFixture builds a bare repo with two diverged branches:
//
//	main: A --- B (base)
//	           \
//	dev:        C --- D (head)
//
// and a "conflict" variant where both branches modify the same file.
func mergeFixture(t *testing.T, conflict bool) string {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	bare := filepath.Join(base, "repo.git")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=M", "GIT_AUTHOR_EMAIL=m@t.test",
			"GIT_COMMITTER_NAME=M", "GIT_COMMITTER_EMAIL=m@t.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run(base, "init", "--bare", "--initial-branch=main", bare)
	run(base, "init", "--initial-branch=main", work)

	write(filepath.Join(work, "shared.txt"), "original\n")
	write(filepath.Join(work, "base-only.txt"), "base content\n")
	run(work, "add", ".")
	run(work, "commit", "-m", "A: initial")

	run(work, "branch", "dev")

	// main gets B.
	write(filepath.Join(work, "shared.txt"), "original\nmain change\n")
	run(work, "commit", "-am", "B: main change")

	// dev gets C and D.
	run(work, "checkout", "dev")
	if conflict {
		write(filepath.Join(work, "shared.txt"), "original\ndev conflicting change\n")
		run(work, "commit", "-am", "C: dev conflicting change")
	} else {
		write(filepath.Join(work, "dev-only.txt"), "dev content\n")
		run(work, "add", ".")
		run(work, "commit", "-m", "C: dev addition")
	}
	write(filepath.Join(work, "another.txt"), "more\n")
	run(work, "add", ".")
	run(work, "commit", "-m", "D: more work")

	run(work, "push", filepath.ToSlash(bare), "main", "dev")
	return bare
}

func TestCommitsBetweenAndDiff(t *testing.T) {
	dir := mergeFixture(t, false)
	e := newExecutor(t)
	ctx := context.Background()

	commits, err := e.CommitsBetween(ctx, dir, "refs/heads/main", "refs/heads/dev", 50)
	if err != nil {
		t.Fatalf("CommitsBetween: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits between = %d, want 2", len(commits))
	}
	if !strings.Contains(commits[0].Subject, "D:") || !strings.Contains(commits[1].Subject, "C:") {
		t.Fatalf("order wrong: %q, %q", commits[0].Subject, commits[1].Subject)
	}

	diff, err := e.DiffBetween(ctx, dir, "refs/heads/main", "refs/heads/dev")
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}
	if !strings.Contains(diff, "dev-only.txt") || !strings.Contains(diff, "another.txt") {
		t.Fatalf("diff missing head-side files:\n%s", diff)
	}
}

func TestCheckMergeCleanAndConflict(t *testing.T) {
	ctx := context.Background()

	t.Run("clean", func(t *testing.T) {
		dir := mergeFixture(t, false)
		e := newExecutor(t)
		status, err := e.CheckMerge(ctx, dir, "refs/heads/main", "refs/heads/dev")
		if err != nil {
			t.Fatalf("CheckMerge: %v", err)
		}
		if !status.Clean || status.TreeSHA == "" {
			t.Fatalf("status = %+v, want clean", status)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		dir := mergeFixture(t, true)
		e := newExecutor(t)
		status, err := e.CheckMerge(ctx, dir, "refs/heads/main", "refs/heads/dev")
		if err != nil {
			t.Fatalf("CheckMerge(conflict): %v", err)
		}
		if status.Clean {
			t.Fatal("conflicting merge reported clean")
		}
		if len(status.Conflicts) == 0 || status.Conflicts[0] != "shared.txt" {
			t.Fatalf("conflicts = %v, want shared.txt", status.Conflicts)
		}
	})
}

func TestMergeStrategies(t *testing.T) {
	ctx := context.Background()
	opts := MergeOptions{
		Strategy:       MergeCommit,
		CommitterName:  "GitHost",
		CommitterEmail: "githost@localhost",
		Message:        "Merge pull request #1",
	}

	t.Run("merge-commit", func(t *testing.T) {
		dir := mergeFixture(t, false)
		e := newExecutor(t)
		sha, err := e.MergePR(ctx, dir, "main", "dev", opts)
		if err != nil {
			t.Fatalf("MergePR: %v", err)
		}
		// The merged head is a merge commit with two parents.
		commit, err := e.Commit(ctx, dir, sha)
		if err != nil {
			t.Fatal(err)
		}
		if len(commit.Parents) != 2 {
			t.Fatalf("parents = %v, want 2", commit.Parents)
		}
		if commit.Subject != "Merge pull request #1" {
			t.Fatalf("subject = %q", commit.Subject)
		}
		// main now contains the dev-side files.
		out := mustExec(t, e, dir, []string{"log", "--format=%s", "refs/heads/main"})
		if !strings.Contains(out, "D: more work") {
			t.Fatalf("main lacks dev commits after merge:\n%s", out)
		}
		// History remains fsck-clean.
		res, err := e.Run(ctx, Options{Args: []string{"fsck", "--strict"}, Dir: dir})
		if err != nil || strings.Contains(string(res.Stdout), "error") {
			t.Fatalf("fsck after merge: %v %s", err, res.Stdout)
		}
	})

	t.Run("squash", func(t *testing.T) {
		dir := mergeFixture(t, false)
		e := newExecutor(t)
		squashOpts := opts
		squashOpts.Strategy = Squash
		sha, err := e.MergePR(ctx, dir, "main", "dev", squashOpts)
		if err != nil {
			t.Fatalf("MergePR(squash): %v", err)
		}
		commit, err := e.Commit(ctx, dir, sha)
		if err != nil {
			t.Fatal(err)
		}
		if len(commit.Parents) != 1 {
			t.Fatalf("squash parents = %v, want 1", commit.Parents)
		}
		// The individual dev commits are NOT on main (squashed away).
		out := mustExec(t, e, dir, []string{"log", "--format=%s", "refs/heads/main"})
		if strings.Contains(out, "D: more work") {
			t.Fatalf("squash leaked individual commits onto main:\n%s", out)
		}
		if !strings.Contains(out, "Merge pull request #1") {
			t.Fatalf("squash commit missing:\n%s", out)
		}
	})

	t.Run("fast-forward", func(t *testing.T) {
		dir := mergeFixture(t, false)
		e := newExecutor(t)
		// main moved (B) so dev is NOT a descendant → must fail.
		ffOpts := opts
		ffOpts.Strategy = FastForwardOnly
		if _, err := e.MergePR(ctx, dir, "main", "dev", ffOpts); !errors.Is(err, ErrNotAncestor) {
			t.Fatalf("ff with diverged base: err = %v, want ErrNotAncestor", err)
		}
		// Fast-forward success case: main with an unborn feature branch
		// that is strictly ahead. Build: feature = main + 1 commit.
		bare := filepath.Join(t.TempDir(), "ff.git")
		work := filepath.Join(t.TempDir(), "w")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		runWork := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = work
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=M", "GIT_AUTHOR_EMAIL=m@t.test",
				"GIT_COMMITTER_NAME=M", "GIT_COMMITTER_EMAIL=m@t.test")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
		runWork("init", "--bare", "--initial-branch=main", bare)
		runWork("init", "--initial-branch=main", work)
		runWork("commit", "--allow-empty", "-m", "one")
		runWork("push", filepath.ToSlash(bare), "main")
		runWork("commit", "--allow-empty", "-m", "two")
		runWork("push", filepath.ToSlash(bare), "HEAD:refs/heads/feature")

		e2, _ := New(nil)
		sha, err := e2.MergePR(ctx, bare, "main", "feature", ffOpts)
		if err != nil {
			t.Fatalf("ff merge: %v", err)
		}
		out := mustExec(t, e2, bare, []string{"log", "--format=%s", "refs/heads/main"})
		if !strings.Contains(out, "two") {
			t.Fatalf("main lacks 'two' after ff:\n%s", out)
		}
		if strings.TrimSpace(mustExec(t, e2, bare, []string{"rev-parse", "refs/heads/main"})) != sha {
			t.Fatal("main does not point at the ff result")
		}
	})
}

// mustExec runs a validated git command through the executor and fails
// on error (test helper only).
func mustExec(t *testing.T, e *Executor, dir string, args []string) string {
	t.Helper()
	res, err := e.Run(context.Background(), Options{Args: args, Dir: dir})
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(res.Stdout)
}

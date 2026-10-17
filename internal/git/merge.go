package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Merge plumbing for pull requests. Everything uses git's own machinery
// (merge-tree for the merge itself, commit-tree for the resulting
// commit, update-ref with CAS for the branch move) — no worktrees, no
// checkouts, no shell.
//
// Requirements: git >= 2.38 (merge-tree --write-tree semantics). The
// doctor checks the floor; these helpers also refuse to run on older
// gits rather than guess.

// ErrMergeConflict reports a merge that does not apply cleanly.
type ErrMergeConflict struct {
	ConflictedFiles []string
}

func (e *ErrMergeConflict) Error() string {
	return fmt.Sprintf("merge conflict in %d file(s): %s",
		len(e.ConflictedFiles), strings.Join(e.ConflictedFiles, ", "))
}

// ErrNotAncestor reports a fast-forward that is not possible.
var ErrNotAncestor = errors.New("head is not a descendant of base (fast-forward not possible)")

// CommitsBetween lists commits reachable from head but not from base,
// newest first (the PR commit list).
func (e *Executor) CommitsBetween(ctx context.Context, dir, base, head string, limit int) ([]Commit, error) {
	if err := ValidateRevision(base); err != nil {
		return nil, err
	}
	if err := ValidateRevision(head); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []string{
		"log",
		"-n", fmt.Sprintf("%d", limit),
		"--format=" + logFieldFormat,
		head,
		"^" + base,
	}
	res, err := e.Run(ctx, Options{Args: args, Dir: dir})
	if err != nil {
		return nil, err
	}
	return parseLogOutput(string(res.Stdout)), nil
}

// DiffBetween returns the textual patch of base...head (two-dot: what
// changed from base to head).
func (e *Executor) DiffBetween(ctx context.Context, dir, base, head string) (string, error) {
	if err := ValidateRevision(base); err != nil {
		return "", err
	}
	if err := ValidateRevision(head); err != nil {
		return "", err
	}
	res, err := e.Run(ctx, Options{
		Args: []string{"diff-tree", "-p", "--no-color", base, head},
		Dir:  dir,
	})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// MergeStatus reports whether base+head merge cleanly.
type MergeStatus struct {
	Clean     bool
	TreeSHA   string   // merged tree (present even when conflicted)
	Conflicts []string // conflicted file paths when !Clean
}

// CheckMerge performs the merge in memory (merge-tree --write-tree):
// no working tree, no ref updates. Exit 0 = clean, exit 1 = conflicts.
func (e *Executor) CheckMerge(ctx context.Context, dir, base, head string) (*MergeStatus, error) {
	if err := e.requireGitMergeTree(ctx); err != nil {
		return nil, err
	}
	if err := ValidateRevision(base); err != nil {
		return nil, err
	}
	if err := ValidateRevision(head); err != nil {
		return nil, err
	}
	res, err := e.Run(ctx, Options{
		Args: []string{"merge-tree", "--write-tree", "--name-only", base, head},
		Dir:  dir,
	})
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
		if len(lines) == 0 || lines[0] == "" {
			return nil, errors.New("merge-tree produced no tree")
		}
		return &MergeStatus{Clean: true, TreeSHA: lines[0]}, nil
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
		if len(lines) == 0 || lines[0] == "" {
			return nil, errors.New("merge-tree produced no tree on conflict")
		}
		var conflicts []string
		// Output: tree, then "CONFLICT (...)" markers and file names.
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "CONFLICT") || strings.HasPrefix(line, "warning:") {
				continue
			}
			conflicts = append(conflicts, line)
		}
		return &MergeStatus{Clean: false, TreeSHA: lines[0], Conflicts: conflicts}, nil
	}
	return nil, err
}

// IsAncestor reports whether base is an ancestor of head.
func (e *Executor) IsAncestor(ctx context.Context, dir, base, head string) (bool, error) {
	if err := ValidateRevision(base); err != nil {
		return false, err
	}
	if err := ValidateRevision(head); err != nil {
		return false, err
	}
	_, err := e.Run(ctx, Options{
		Args: []string{"merge-base", "--is-ancestor", base, head},
		Dir:  dir,
	})
	if err == nil {
		return true, nil
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		return false, nil
	}
	return false, err
}

// RevParseSHA resolves a revision to a full object id.
func (e *Executor) RevParseSHA(ctx context.Context, dir, rev string) (string, error) {
	if err := ValidateRevision(rev); err != nil {
		return "", err
	}
	res, err := e.Run(ctx, Options{
		Args: []string{"rev-parse", "--verify", rev},
		Dir:  dir,
	})
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if err := ValidateHash(sha); err != nil {
		return "", err
	}
	return sha, nil
}

// MergeStrategy names the supported PR merge strategies.
type MergeStrategy string

const (
	// MergeCommit creates a true merge commit with two parents.
	MergeCommit MergeStrategy = "merge-commit"
	// Squash flattens head into a single commit on base.
	Squash MergeStrategy = "squash"
	// FastForwardOnly moves base to head when base is an ancestor.
	FastForwardOnly MergeStrategy = "fast-forward-only"
)

// ValidMergeStrategy reports whether a strategy is supported.
func ValidMergeStrategy(s string) bool {
	return s == string(MergeCommit) || s == string(Squash) || s == string(FastForwardOnly)
}

// MergeOptions describe one PR merge.
type MergeOptions struct {
	Strategy MergeStrategy
	// Committer identity recorded on platform-created commits.
	CommitterName  string
	CommitterEmail string
	// Message for merge/squash commits (ignored by fast-forward).
	Message string
}

// MergePR executes a PR merge and returns the new base branch head.
//
// All ref moves are compare-and-swap: if base moved since the PR was
// created, the merge fails with ErrNotAncestor (or a dirty-tree error)
// instead of overwriting anyone's work.
func (e *Executor) MergePR(ctx context.Context, dir, baseBranch, headBranch string, opts MergeOptions) (string, error) {
	if err := ValidateBranchName(baseBranch); err != nil {
		return "", err
	}
	if err := ValidateBranchName(headBranch); err != nil {
		return "", err
	}
	baseRef := "refs/heads/" + baseBranch
	headRef := "refs/heads/" + headBranch

	baseSHA, err := e.RevParseSHA(ctx, dir, baseRef)
	if err != nil {
		return "", fmt.Errorf("base branch: %w", err)
	}
	headSHA, err := e.RevParseSHA(ctx, dir, headRef)
	if err != nil {
		return "", fmt.Errorf("head branch: %w", err)
	}
	if baseSHA == headSHA {
		return "", errors.New("base and head point at the same commit")
	}

	gitConfig := []string{
		"user.name=" + opts.CommitterName,
		"user.email=" + opts.CommitterEmail,
	}

	switch opts.Strategy {
	case MergeCommit:
		status, err := e.CheckMerge(ctx, dir, baseSHA, headSHA)
		if err != nil {
			return "", err
		}
		if !status.Clean {
			return "", &ErrMergeConflict{ConflictedFiles: status.Conflicts}
		}
		mergeSHA, err := e.commitTree(ctx, dir, status.TreeSHA,
			[]string{baseSHA, headSHA}, opts.Message, gitConfig)
		if err != nil {
			return "", err
		}
		if err := e.updateRefCAS(ctx, dir, baseRef, mergeSHA, baseSHA); err != nil {
			return "", err
		}
		return mergeSHA, nil

	case Squash:
		status, err := e.CheckMerge(ctx, dir, baseSHA, headSHA)
		if err != nil {
			return "", err
		}
		if !status.Clean {
			return "", &ErrMergeConflict{ConflictedFiles: status.Conflicts}
		}
		squashSHA, err := e.commitTree(ctx, dir, status.TreeSHA,
			[]string{baseSHA}, opts.Message, gitConfig)
		if err != nil {
			return "", err
		}
		if err := e.updateRefCAS(ctx, dir, baseRef, squashSHA, baseSHA); err != nil {
			return "", err
		}
		return squashSHA, nil

	case FastForwardOnly:
		ok, err := e.IsAncestor(ctx, dir, baseSHA, headSHA)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrNotAncestor
		}
		if err := e.updateRefCAS(ctx, dir, baseRef, headSHA, baseSHA); err != nil {
			return "", err
		}
		return headSHA, nil
	}
	return "", fmt.Errorf("unsupported merge strategy %q", opts.Strategy)
}

// commitTree creates a commit object from a tree with the given parents.
func (e *Executor) commitTree(ctx context.Context, dir, treeSHA string, parents []string, message string, gitConfig []string) (string, error) {
	if err := ValidateHash(treeSHA); err != nil {
		return "", err
	}
	if message == "" {
		message = "Merge pull request"
	}
	args := []string{"commit-tree", treeSHA}
	for _, p := range parents {
		if err := ValidateHash(p); err != nil {
			return "", err
		}
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)
	res, err := e.Run(ctx, Options{Args: args, Dir: dir, Config: gitConfig})
	if err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if err := ValidateHash(sha); err != nil {
		return "", err
	}
	return sha, nil
}

// updateRefCAS moves ref to newSHA only if it still equals oldSHA.
func (e *Executor) updateRefCAS(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	if err := ValidateRefPath(ref); err != nil {
		return err
	}
	if err := ValidateHash(newSHA); err != nil {
		return err
	}
	if err := ValidateHash(oldSHA); err != nil {
		return err
	}
	_, err := e.Run(ctx, Options{
		Args: []string{"update-ref", ref, newSHA, oldSHA},
		Dir:  dir,
	})
	return err
}

// requireGitMergeTree guards the modern merge-tree behavior (2.38+).
func (e *Executor) requireGitMergeTree(ctx context.Context) error {
	ver, err := e.Version(ctx)
	if err != nil {
		return err
	}
	if !AtLeast(ver, 2, 38) {
		return fmt.Errorf("pull request merges require git >= 2.38 (found %s)", ver)
	}
	return nil
}

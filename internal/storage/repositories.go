// Package storage manages on-disk bare repositories.
//
// Security invariant (threat T2 in docs/security/threat-model.md):
// repositories are addressed exclusively by their internal 32-hex UUID;
// user-facing owner/name pairs never reach the filesystem. Every path is
// re-validated to stay inside the repositories root after joining.
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"githost.dev/githost/internal/git"
)

// uuidPattern is the on-disk identity format.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Repositories is the bare-repository store rooted at a single directory.
type Repositories struct {
	root string
	git  *git.Executor
}

// NewRepositories prepares (and creates when missing) the repositories
// root directory.
func NewRepositories(root string, executor *git.Executor) (*Repositories, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create repositories root: %w", err)
	}
	return &Repositories{root: filepath.Clean(root), git: executor}, nil
}

// Root returns the repositories root directory.
func (r *Repositories) Root() string {
	return r.root
}

// Path maps a repository UUID to its bare-repository directory. The joined
// path is verified to remain inside the root (defense in depth on top of
// the UUID format check).
func (r *Repositories) Path(uuid string) (string, error) {
	if !uuidPattern.MatchString(uuid) {
		return "", fmt.Errorf("repository uuid %q: invalid format", uuid)
	}
	path := filepath.Join(r.root, uuid+".git")
	clean := filepath.Clean(path)
	if clean != r.root && !strings.HasPrefix(clean, r.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("repository path %q escapes the repositories root", path)
	}
	return clean, nil
}

// Create initializes a bare repository for the given UUID.
func (r *Repositories) Create(ctx context.Context, uuid, defaultBranch string) (string, error) {
	if err := git.ValidateBranchName(defaultBranch); err != nil {
		return "", err
	}
	path, err := r.Path(uuid)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("repository directory %s already exists", path)
	}
	if _, err := r.git.Run(ctx, git.Options{
		Args: []string{"init", "--bare", "--initial-branch=" + defaultBranch, path},
	}); err != nil {
		return "", fmt.Errorf("init bare repository: %w", err)
	}
	return path, nil
}

// Delete removes the bare repository directory. The directory is first
// renamed out of the way so a partially failed removal cannot leave a
// half-alive repository behind.
func (r *Repositories) Delete(uuid string) error {
	path, err := r.Path(uuid)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	trash := filepath.Join(r.root, ".trash-"+uuid)
	if err := os.Rename(path, trash); err != nil {
		return fmt.Errorf("move repository to trash: %w", err)
	}
	if err := os.RemoveAll(trash); err != nil {
		// The repository is already unreachable (renamed); surface the
		// cleanup failure without pretending the delete succeeded.
		return fmt.Errorf("remove trashed repository: %w", err)
	}
	return nil
}

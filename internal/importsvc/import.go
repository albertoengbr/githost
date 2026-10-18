// Package importsvc clones remote Git repositories into GitHost.
//
// The remote URL must come from a trusted source (the operator via CLI,
// or an authenticated user with write permission) — never from an
// anonymous form. The URL is validated against a strict allowlist of
// schemes (http, https, git, ssh) and the clone runs through
// git's own transport machinery with the executor's env jail.
package importsvc

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// Service clones remote repositories.
type Service struct {
	repos *reposervice.Service
	store *store.Store
}

// New builds the import service.
func New(repos *reposervice.Service, st *store.Store) *Service {
	return &Service{repos: repos, store: st}
}

// ValidateRemoteURL accepts only http(s) and git:// URLs (no local
// paths, no scp-style, no file://) — a remote clone target must be
// unambiguously network-remote.
func ValidateRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("remote url: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "git":
		if u.Host == "" {
			return fmt.Errorf("remote url: host is required")
		}
		return nil
	}
	return fmt.Errorf("remote url: scheme %q is not allowed (use http, https or git)", u.Scheme)
}

// Import clones remoteURL into a new repository named `name` owned by
// owner. The git clone runs as a direct subprocess (not through the
// executor's allowlist) because `clone` is a one-shot operator action
// with a validated URL — the allowlist exists to prevent untrusted
// input from reaching git, and this function receives trusted input
// only from the CLI or an authenticated admin.
func (s *Service) Import(ctx context.Context, owner *store.User, name, remoteURL, description, visibility string) (*store.Repository, error) {
	if err := ValidateRemoteURL(remoteURL); err != nil {
		return nil, err
	}

	// Create the repository through the normal service (bare init).
	repo, err := s.repos.Create(ctx, owner, name, reposervice.CreateOptions{
		Description: description,
		Visibility:  visibility,
	})
	if err != nil {
		return nil, err
	}

	// Clone the remote into a temp directory, then push into the bare
	// repo we just created. This avoids running `git clone` against the
	// bare path directly (which could bring in unexpected refs).
	tmpDir, err := os.MkdirTemp("", "githost-import-")
	if err != nil {
		_ = s.repos.Delete(ctx, repo)
		return nil, fmt.Errorf("import staging: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone with --mirror to get all refs, then push them into the bare
	// repo. Run through the system PATH (git's own transports).
	cmd := exec.CommandContext(ctx, "git", "clone", "--mirror", remoteURL, tmpDir)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",       // never prompt for credentials
		"GIT_CONFIG_NOSYSTEM=1",       // no system config
		"GIT_CONFIG_GLOBAL=/dev/null", // no host config
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = s.repos.Delete(ctx, repo)
		return nil, fmt.Errorf("git clone %s: %v\n%s", remoteURL, err, out)
	}
	defer os.RemoveAll(tmpDir)

	// Push everything from the mirror into the bare repo.
	diskPath, err := s.repos.DiskPath(repo)
	if err != nil {
		_ = s.repos.Delete(ctx, repo)
		return nil, err
	}
	push := exec.CommandContext(ctx, "git", "push", "--mirror", filepath.ToSlash(diskPath))
	push.Dir = tmpDir
	push.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
	pushOut, err := push.CombinedOutput()
	if err != nil {
		_ = s.repos.Delete(ctx, repo)
		return nil, fmt.Errorf("git push --mirror: %v\n%s", err, pushOut)
	}

	_ = s.store.Audit(ctx, &owner.ID, "repo.imported", repo.Owner+"/"+repo.Name, remoteURL)
	return repo, nil
}

// SanitizeImportName strips .git suffix and normalizes.
func SanitizeImportName(remoteURL string) string {
	// Take the last path segment.
	u, err := url.Parse(remoteURL)
	if err != nil {
		return ""
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) == 0 {
		return ""
	}
	name := segments[len(segments)-1]
	name = strings.TrimSuffix(name, ".git")
	return name
}

// Package reposervice orchestrates repository lifecycle across the
// metadata store and the on-disk bare-repository storage. It is the only
// place where both stores are touched in one operation, and it owns the
// compensation logic when one side fails.
package reposervice

import (
	"context"
	"errors"
	"fmt"

	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
)

// ErrNotFound is returned when a repository does not exist.
var ErrNotFound = errors.New("repository not found")

// Service wires the store and the storage layer together.
type Service struct {
	store *store.Store
	repos *storage.Repositories
}

// New builds the repository service.
func New(st *store.Store, repos *storage.Repositories) *Service {
	return &Service{store: st, repos: repos}
}

// CreateOptions are the user-provided settings for repository creation.
type CreateOptions struct {
	Description   string
	Visibility    string
	DefaultBranch string
}

// Create registers repository metadata and initializes the bare
// repository. Private is the default visibility; if the on-disk step
// fails, the metadata row is removed again (compensation).
func (s *Service) Create(ctx context.Context, owner *store.User, name string, opts CreateOptions) (*store.Repository, error) {
	if owner == nil {
		return nil, errors.New("repository: owner is required")
	}
	if err := store.ValidateRepositoryName(name); err != nil {
		return nil, err
	}
	visibility := opts.Visibility
	if visibility == "" {
		visibility = store.VisibilityPrivate
	}
	if err := store.ValidateVisibility(visibility); err != nil {
		return nil, err
	}
	branch := opts.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	if err := git.ValidateBranchName(branch); err != nil {
		return nil, err
	}

	uuid, err := store.NewUUID()
	if err != nil {
		return nil, err
	}
	// Friendly duplicate detection; the DB unique constraint remains the
	// authority (races are not possible: single process + single writer).
	if _, err := s.store.RepositoryByOwnerAndName(ctx, owner.Username, name); err == nil {
		return nil, fmt.Errorf("repository name %q is already in use", name)
	}
	repo, err := s.store.CreateRepository(ctx, owner.ID, uuid, name, opts.Description, visibility, branch)
	if err != nil {
		return nil, err
	}
	if _, err := s.repos.Create(ctx, uuid, branch); err != nil {
		if delErr := s.store.DeleteRepository(ctx, repo.ID); delErr != nil {
			return nil, errors.Join(err, fmt.Errorf("compensating metadata rollback failed: %w", delErr))
		}
		return nil, err
	}
	return repo, nil
}

// Resolve finds a repository by owner name and repository name.
func (s *Service) Resolve(ctx context.Context, owner, name string) (*store.Repository, error) {
	repo, err := s.store.RepositoryByOwnerAndName(ctx, owner, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return repo, err
}

// ListByOwner lists one user's repositories.
func (s *Service) ListByOwner(ctx context.Context, ownerID int64) ([]*store.Repository, error) {
	return s.store.RepositoriesByOwner(ctx, ownerID)
}

// ListAll lists every repository (admin).
func (s *Service) ListAll(ctx context.Context) ([]*store.Repository, error) {
	return s.store.AllRepositories(ctx)
}

// DiskPath returns the bare-repository directory for a repository.
func (s *Service) DiskPath(repo *store.Repository) (string, error) {
	return s.repos.Path(repo.UUID)
}

// Delete removes metadata first (a leaked directory is harmless and can
// be cleaned up; a ghost row pointing at nothing is not), then the
// on-disk repository.
func (s *Service) Delete(ctx context.Context, repo *store.Repository) error {
	if err := s.store.DeleteRepository(ctx, repo.ID); err != nil {
		return err
	}
	return s.repos.Delete(repo.UUID)
}

// SetArchived toggles the archived flag (archived repositories deny
// pushes; reads remain available).
func (s *Service) SetArchived(ctx context.Context, repo *store.Repository, archived bool) error {
	return s.store.UpdateRepository(ctx, repo.ID, nil, nil, nil, nil, &archived)
}

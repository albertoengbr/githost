// Package permissions implements GitHost's authorization model.
//
// Rules (docs/requirements.md SEC-005, threat T3):
//   - anonymous access: read on public repositories only;
//   - instance admins: full access to everything;
//   - owners: admin on their repositories;
//   - members: the permission granted by membership;
//   - everyone else: public read or nothing.
//
// Archived repositories allow reads but deny writes (checked by callers
// via CanPush).
package permissions

import (
	"context"

	"githost.dev/githost/internal/store"
)

// Level is an access level, ordered from least to most permissive.
type Level int

// Access levels.
const (
	None  Level = iota
	Read  Level = iota
	Write Level = iota
	Admin Level = iota
)

// String renders the level for logs and audit events.
func (l Level) String() string {
	switch l {
	case None:
		return "none"
	case Read:
		return "read"
	case Write:
		return "write"
	case Admin:
		return "admin"
	}
	return "unknown"
}

// AtLeast reports whether the level satisfies a minimum requirement.
func (l Level) AtLeast(min Level) bool {
	return l >= min
}

// CanPush reports whether writes are allowed for this level on the
// repository state. Archived repositories never accept pushes.
func CanPush(level Level, archived bool) bool {
	return !archived && level >= Write
}

// Service resolves effective access levels against the metadata store.
type Service struct {
	store *store.Store
}

// New builds the authorization service.
func New(st *store.Store) *Service {
	return &Service{store: st}
}

// ForRepository returns the effective access level of the user (nil means
// anonymous) on the repository. Precedence: instance admin > owner >
// explicit membership > public-read floor.
func (s *Service) ForRepository(ctx context.Context, user *store.User, repo *store.Repository) (Level, error) {
	if user == nil {
		if repo.Visibility == store.VisibilityPublic {
			return Read, nil
		}
		return None, nil
	}
	if user.IsAdmin {
		return Admin, nil
	}
	if user.ID == repo.OwnerID {
		return Admin, nil
	}
	if level, ok, err := s.memberLevel(ctx, user, repo); err != nil {
		return None, err
	} else if ok {
		return level, nil
	}
	if repo.Visibility == store.VisibilityPublic {
		return Read, nil
	}
	return None, nil
}

// memberLevel maps a membership row to an access level. Owners and
// instance admins are handled before this runs, so it is strictly about
// repository_members rows.
func (s *Service) memberLevel(ctx context.Context, user *store.User, repo *store.Repository) (Level, bool, error) {
	permission, ok, err := s.store.MemberPermission(ctx, repo.ID, user.ID)
	if err != nil || !ok {
		return None, false, err
	}
	switch permission {
	case store.PermissionRead:
		return Read, true, nil
	case store.PermissionWrite:
		return Write, true, nil
	case store.PermissionAdmin:
		return Admin, true, nil
	}
	return None, false, nil
}

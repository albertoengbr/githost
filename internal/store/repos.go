package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Repository is a hosted project. UUID is the on-disk identity
// (<uuid>.git under the repositories root); OwnerID references users.
type Repository struct {
	ID            int64
	UUID          string
	OwnerID       int64
	Owner         string // joined from users, for convenience lookups
	Name          string
	Description   string
	Visibility    string
	DefaultBranch string
	IsArchived    bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const repositoryColumns = "r.id, r.uuid, r.owner_id, u.username, r.name, r.description, r.visibility, r.default_branch, r.is_archived, r.created_at, r.updated_at"

func scanRepository(row interface{ Scan(...any) error }) (*Repository, error) {
	var r Repository
	var archived int
	var created, updated string
	err := row.Scan(&r.ID, &r.UUID, &r.OwnerID, &r.Owner, &r.Name, &r.Description,
		&r.Visibility, &r.DefaultBranch, &archived, &created, &updated)
	if err != nil {
		return nil, err
	}
	r.IsArchived = archived != 0
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	return &r, nil
}

// NewUUID returns a random 32-char hex identity for on-disk repositories.
func NewUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate repository uuid: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// CreateRepository inserts repository metadata with a fresh UUID. The
// caller (repository service) creates the bare repo on disk and owns the
// compensation logic if that step fails.
func (s *Store) CreateRepository(ctx context.Context, ownerID int64, uuid, name, description, visibility, defaultBranch string) (*Repository, error) {
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO repositories (uuid, owner_id, name, description, visibility, default_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid, ownerID, name, description, visibility, defaultBranch, now, now)
	if err != nil {
		return nil, fmt.Errorf("create repository: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.RepositoryByID(ctx, id)
}

// RepositoryByID looks up a repository by primary key.
func (s *Store) RepositoryByID(ctx context.Context, id int64) (*Repository, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+repositoryColumns+" FROM repositories r JOIN users u ON u.id = r.owner_id WHERE r.id = ?", id)
	r, err := scanRepository(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RepositoryByOwnerAndName resolves "owner/name" to a repository. This is
// the only sanctioned translation from user-facing names to identities —
// transports must use the returned UUID, never the name, on disk.
func (s *Store) RepositoryByOwnerAndName(ctx context.Context, owner, name string) (*Repository, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+repositoryColumns+
			" FROM repositories r JOIN users u ON u.id = r.owner_id WHERE u.username = ? AND r.name = ?",
		NormalizeUsername(owner), name)
	r, err := scanRepository(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RepositoriesByOwner lists a user's repositories ordered by name.
func (s *Store) RepositoriesByOwner(ctx context.Context, ownerID int64) ([]*Repository, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+repositoryColumns+
			" FROM repositories r JOIN users u ON u.id = r.owner_id WHERE r.owner_id = ? ORDER BY r.name", ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []*Repository
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// AllRepositories lists every repository (admin listing).
func (s *Store) AllRepositories(ctx context.Context) ([]*Repository, error) {
	return s.queryRepositories(ctx,
		"SELECT "+repositoryColumns+
			" FROM repositories r JOIN users u ON u.id = r.owner_id ORDER BY r.owner_id, r.name")
}

// RepositoriesVisibleTo lists repositories the viewer may read: public
// repositories plus the viewer's own and their memberships. Anonymous
// viewers see public repositories only.
func (s *Store) RepositoriesVisibleTo(ctx context.Context, user *User) ([]*Repository, error) {
	if user == nil {
		return s.queryRepositories(ctx,
			"SELECT "+repositoryColumns+
				" FROM repositories r JOIN users u ON u.id = r.owner_id"+
				" WHERE r.visibility = 'public' ORDER BY r.updated_at DESC")
	}
	return s.queryRepositories(ctx,
		"SELECT "+repositoryColumns+
			" FROM repositories r JOIN users u ON u.id = r.owner_id"+
			" WHERE r.visibility = 'public' OR r.owner_id = ?"+
			"    OR EXISTS (SELECT 1 FROM repository_members m WHERE m.repository_id = r.id AND m.user_id = ?)"+
			" ORDER BY r.updated_at DESC",
		user.ID, user.ID)
}

// queryRepositories runs a repository listing query and scans rows.
func (s *Store) queryRepositories(ctx context.Context, query string, args ...any) ([]*Repository, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []*Repository
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// DeleteRepository removes repository metadata (cascade removes members).
func (s *Store) DeleteRepository(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM repositories WHERE id = ?", id)
	return err
}

// UpdateRepository applies partial updates to repository metadata.
func (s *Store) UpdateRepository(ctx context.Context, id int64, name, description, visibility, defaultBranch *string, isArchived *bool) error {
	// Simple, explicit updates; the combination space is tiny.
	sets := []string{"updated_at = ?"}
	args := []any{nowUTC()}
	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *description)
	}
	if visibility != nil {
		sets = append(sets, "visibility = ?")
		args = append(args, *visibility)
	}
	if defaultBranch != nil {
		sets = append(sets, "default_branch = ?")
		args = append(args, *defaultBranch)
	}
	if isArchived != nil {
		sets = append(sets, "is_archived = ?")
		archived := 0
		if *isArchived {
			archived = 1
		}
		args = append(args, archived)
	}
	args = append(args, id)
	_, err := s.db.ExecContext(ctx,
		"UPDATE repositories SET "+joinStrings(sets, ", ")+" WHERE id = ?", args...)
	return err
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// ------------------------------------------------------- repository members

// MemberPermission returns the permission a user has on a repository
// through explicit membership ("", false when not a member).
func (s *Store) MemberPermission(ctx context.Context, repositoryID, userID int64) (string, bool, error) {
	var permission string
	err := s.db.QueryRowContext(ctx,
		"SELECT permission FROM repository_members WHERE repository_id = ? AND user_id = ?",
		repositoryID, userID).Scan(&permission)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return permission, true, nil
}

// AddMember grants a repository membership. Inserting a duplicate row is
// an error (SQLite enforces the primary key).
func (s *Store) AddMember(ctx context.Context, repositoryID, userID int64, permission string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO repository_members (repository_id, user_id, permission, created_at) VALUES (?, ?, ?, ?)",
		repositoryID, userID, permission, nowUTC())
	return err
}

// RemoveMember revokes a membership.
func (s *Store) RemoveMember(ctx context.Context, repositoryID, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM repository_members WHERE repository_id = ? AND user_id = ?", repositoryID, userID)
	return err
}

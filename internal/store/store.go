// Package store provides typed data access for GitHost's SQLite metadata.
//
// The store is intentionally thin: SQL lives here, business rules live in
// the services that call it. Domain-entity splitting (internal/domain) is
// deferred until the web phases grow real behavior; for F1 this single
// cohesive package keeps the layering honest without ceremony.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the metadata database.
type Store struct {
	db *sql.DB
}

// New builds a Store on an open database.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB exposes the underlying handle (used by services for one-off queries).
func (s *Store) DB() *sql.DB {
	return s.db
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---------------------------------------------------------------- users

// User is an account. PasswordHash is an Argon2id PHC string ("" when the
// account has no password).
type User struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const userColumns = "id, username, email, password_hash, is_admin, created_at, updated_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var isAdmin int
	var created, updated string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &isAdmin, &created, &updated); err != nil {
		return nil, err
	}
	u.IsAdmin = isAdmin != 0
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return &u, nil
}

// CreateUser inserts a new account. Username must already be validated
// (ValidateUsername) and normalized to lowercase by the caller.
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string, isAdmin bool) (*User, error) {
	admin := 0
	if isAdmin {
		admin = 1
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO users (username, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		username, email, passwordHash, admin, now, now)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{
		ID:           id,
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		IsAdmin:      isAdmin,
		CreatedAt:    parseTime(now),
		UpdatedAt:    parseTime(now),
	}, nil
}

// UserByID looks up an account by primary key.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// UserByUsername looks up an account by (lowercase) username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE username = ?", strings.ToLower(username))
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// Users lists all accounts ordered by name.
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountUsers reports the number of accounts (used by admin bootstrap logic).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountAdmins reports how many accounts hold the admin flag. Used to keep
// at least one administrator on the instance.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE is_admin = 1").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// UpdateUserAccount edits the mutable account fields (email, admin flag).
// The username is immutable: it keys git remotes and repository paths.
func (s *Store) UpdateUserAccount(ctx context.Context, userID int64, email string, isAdmin bool) error {
	admin := 0
	if isAdmin {
		admin = 1
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE users SET email = ?, is_admin = ?, updated_at = ? WHERE id = ?",
		email, admin, nowUTC(), userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser removes an account and everything cascading from it
// (sessions, SSH keys, tokens). The caller MUST refuse deletion when the
// user still owns repositories: repositories cascade too, and authored
// rows without a cascade (issues, PRs) block the delete via referential
// integrity — that failure is surfaced to the admin, not swallowed.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", userID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// SetPassword replaces a user's password hash.
func (s *Store) SetPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		passwordHash, nowUTC(), userID)
	return err
}

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------- ssh keys

// SSHKey is a registered public key.
type SSHKey struct {
	ID          int64
	UserID      int64
	Fingerprint string
	PublicKey   string // canonical single-line authorized-key format
	Title       string
	CreatedAt   time.Time
}

// AddSSHKey registers a public key. The caller is responsible for the key
// being well-formed (internal/auth validates and fingerprints).
func (s *Store) AddSSHKey(ctx context.Context, userID int64, fingerprint, publicKey, title string) (*SSHKey, error) {
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO ssh_keys (user_id, fingerprint, public_key, title, created_at) VALUES (?, ?, ?, ?, ?)",
		userID, fingerprint, publicKey, title, now)
	if err != nil {
		return nil, fmt.Errorf("add ssh key: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &SSHKey{
		ID:          id,
		UserID:      userID,
		Fingerprint: fingerprint,
		PublicKey:   publicKey,
		Title:       title,
		CreatedAt:   parseTime(now),
	}, nil
}

// SSHKeyByFingerprint resolves a public key to its registered record.
func (s *Store) SSHKeyByFingerprint(ctx context.Context, fingerprint string) (*SSHKey, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, user_id, fingerprint, public_key, title, created_at FROM ssh_keys WHERE fingerprint = ?",
		fingerprint)
	var k SSHKey
	var created string
	err := row.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.PublicKey, &k.Title, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.CreatedAt = parseTime(created)
	return &k, nil
}

// SSHKeysByUser lists a user's registered keys.
func (s *Store) SSHKeysByUser(ctx context.Context, userID int64) ([]*SSHKey, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, user_id, fingerprint, public_key, title, created_at FROM ssh_keys WHERE user_id = ? ORDER BY id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*SSHKey
	for rows.Next() {
		var k SSHKey
		var created string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.PublicKey, &k.Title, &created); err != nil {
			return nil, err
		}
		k.CreatedAt = parseTime(created)
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// DeleteSSHKey removes a key (revocation).
func (s *Store) DeleteSSHKey(ctx context.Context, id int64, userID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM ssh_keys WHERE id = ? AND user_id = ?", id, userID)
	return err
}

// ----------------------------------------------------------- access tokens

// AccessToken is a registered API/Git credential. Only TokenHash is
// stored; the plaintext value exists once, at creation time.
type AccessToken struct {
	ID          int64
	UserID      int64
	Name        string
	TokenHash   string
	TokenPrefix string
	Scopes      string
	ExpiresAt   time.Time // zero when non-expiring
	RevokedAt   time.Time // zero when active
	CreatedAt   time.Time
}

// Active reports whether the token can be used.
func (t *AccessToken) Active() bool {
	return t.RevokedAt.IsZero() && (t.ExpiresAt.IsZero() || time.Now().Before(t.ExpiresAt))
}

// HashToken returns the hex SHA-256 of a token value (the stored form).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateToken registers a token (hash only).
func (s *Store) CreateToken(ctx context.Context, userID int64, name, tokenHash, tokenPrefix, scopes string, expiresAt *time.Time) (*AccessToken, error) {
	now := nowUTC()
	var expires any
	if expiresAt != nil {
		expires = expiresAt.UTC().Format(time.RFC3339)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO access_tokens (user_id, name, token_hash, token_prefix, scopes, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, name, tokenHash, tokenPrefix, scopes, expires, now)
	if err != nil {
		return nil, fmt.Errorf("create token: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &AccessToken{
		ID:          id,
		UserID:      userID,
		Name:        name,
		TokenHash:   tokenHash,
		TokenPrefix: tokenPrefix,
		Scopes:      scopes,
		CreatedAt:   parseTime(now),
	}, nil
}

// TokenByHash resolves a presented token value to its record.
func (s *Store) TokenByHash(ctx context.Context, tokenHash string) (*AccessToken, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, user_id, name, token_hash, token_prefix, scopes, expires_at, revoked_at, created_at FROM access_tokens WHERE token_hash = ?",
		tokenHash)
	var t AccessToken
	var expires, revoked, created sql.NullString
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.TokenPrefix, &t.Scopes, &expires, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if expires.Valid {
		t.ExpiresAt = parseTime(expires.String)
	}
	if revoked.Valid {
		t.RevokedAt = parseTime(revoked.String)
	}
	t.CreatedAt = parseTime(created.String)
	return &t, nil
}

// TokensByUser lists a user's tokens (hashes included only for admin
// display; the caller decides what to render).
func (s *Store) TokensByUser(ctx context.Context, userID int64) ([]*AccessToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, token_hash, token_prefix, scopes, expires_at, revoked_at, created_at
		 FROM access_tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []*AccessToken
	for rows.Next() {
		var t AccessToken
		var expires, revoked, created sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.TokenPrefix, &t.Scopes, &expires, &revoked, &created); err != nil {
			return nil, err
		}
		if expires.Valid {
			t.ExpiresAt = parseTime(expires.String)
		}
		if revoked.Valid {
			t.RevokedAt = parseTime(revoked.String)
		}
		t.CreatedAt = parseTime(created.String)
		tokens = append(tokens, &t)
	}
	return tokens, rows.Err()
}

// RevokeToken marks a token unusable.
func (s *Store) RevokeToken(ctx context.Context, id int64, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE access_tokens SET revoked_at = ? WHERE id = ? AND user_id = ?", nowUTC(), id, userID)
	return err
}

// ------------------------------------------------------------------- audit

// Audit records a security-relevant event. Actor may be nil (anonymous).
func (s *Store) Audit(ctx context.Context, actorID *int64, action, target, detail string) error {
	var actor any
	if actorID != nil {
		actor = *actorID
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO audit_events (actor_id, action, target, detail, created_at) VALUES (?, ?, ?, ?, ?)",
		actor, action, target, detail, nowUTC())
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------- sessions

// Session is a web login session. The cookie carries a random token; only
// its SHA-256 hash (ID) is stored.
type Session struct {
	ID        string // sha256 hex of the cookie token
	UserID    int64
	ExpiresAt time.Time
	CreatedAt time.Time
	IP        string
	UserAgent string
}

// SessionUser is the result of resolving a session: both the session row
// and the logged-in user.
type SessionUser struct {
	Session Session
	User    User
}

// CreateSession records a login session. The caller keeps the plaintext
// token (cookie) and passes its hash as id.
func (s *Store) CreateSession(ctx context.Context, id string, userID int64, expiresAt time.Time, ip, userAgent string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (id, user_id, expires_at, created_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)",
		id, userID, expiresAt.UTC().Format(time.RFC3339), nowUTC(), ip, userAgent)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionByHash resolves a session token hash, joining the user and
// enforcing expiry.
func (s *Store) SessionByHash(ctx context.Context, id string) (*SessionUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.expires_at, s.created_at, s.ip, s.user_agent,
		        u.id, u.username, u.email, u.password_hash, u.is_admin, u.created_at, u.updated_at
		 FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ?`, id)
	var su SessionUser
	var sExpires, sCreated, uCreated, uUpdated string
	var isAdmin int
	err := row.Scan(&su.Session.ID, &su.Session.UserID, &sExpires, &sCreated, &su.Session.IP, &su.Session.UserAgent,
		&su.User.ID, &su.User.Username, &su.User.Email, &su.User.PasswordHash, &isAdmin, &uCreated, &uUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	su.User.IsAdmin = isAdmin != 0
	su.Session.ExpiresAt = parseTime(sExpires)
	su.Session.CreatedAt = parseTime(sCreated)
	su.User.CreatedAt = parseTime(uCreated)
	su.User.UpdatedAt = parseTime(uUpdated)
	if su.Session.ExpiresAt.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return &su, nil
}

// DeleteSession removes one session (logout).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	return err
}

// DeleteUserSessions removes all sessions of a user (password change etc.).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

// PruneSessions removes expired sessions; returns rows removed.
func (s *Store) PruneSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE expires_at < ?", nowUTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

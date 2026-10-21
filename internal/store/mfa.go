package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// --------------------------------------------------------------- MFA/TOTP

// SetTOTPSecret stores the (encrypted) TOTP secret for a user.
func (s *Store) SetTOTPSecret(ctx context.Context, userID int64, encryptedSecret string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE users SET totp_secret = ?, updated_at = ? WHERE id = ?",
		encryptedSecret, nowUTC(), userID)
	return err
}

// GetTOTPSecret retrieves the encrypted TOTP secret ("" when unset).
func (s *Store) GetTOTPSecret(ctx context.Context, userID int64) (string, error) {
	var secret string
	err := s.db.QueryRowContext(ctx,
		"SELECT totp_secret FROM users WHERE id = ?", userID).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return secret, err
}

// SetMFAEnabled toggles the MFA flag (requires totp_secret to be set).
func (s *Store) SetMFAEnabled(ctx context.Context, userID int64, enabled bool) error {
	if enabled {
		// Verify a secret exists before enabling.
		var secret string
		err := s.db.QueryRowContext(ctx,
			"SELECT totp_secret FROM users WHERE id = ?", userID).Scan(&secret)
		if err != nil || secret == "" {
			return fmt.Errorf("cannot enable MFA without a TOTP secret")
		}
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE users SET mfa_enabled = ?, updated_at = ? WHERE id = ?",
		boolToInt(enabled), nowUTC(), userID)
	return err
}

// IsMFAEnabled reports whether MFA is active for the user.
func (s *Store) IsMFAEnabled(ctx context.Context, userID int64) (bool, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx,
		"SELECT mfa_enabled FROM users WHERE id = ?", userID).Scan(&enabled)
	if err != nil {
		return false, err
	}
	return enabled != 0, nil
}

// ---------------------------------------------------------- recovery codes

// RecoveryCode is a one-time-use code for MFA lockout.
type RecoveryCode struct {
	ID        int64
	UserID    int64
	CodeHash  string
	UsedAt    time.Time // zero when unused
	CreatedAt time.Time
}

// AddRecoveryCodes stores hashed recovery codes for a user,
// replacing any previous set.
func (s *Store) AddRecoveryCodes(ctx context.Context, userID int64, hashedCodes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM recovery_codes WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, hash := range hashedCodes {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)",
			userID, hash, nowUTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TryRecoveryCode consumes an unused recovery code.
// Returns true if the code was valid and consumed.
func (s *Store) TryRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	hash := HashToken(code)
	res, err := s.db.ExecContext(ctx,
		"UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL",
		nowUTC(), userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UnusedRecoveryCodes counts how many recovery codes remain.
func (s *Store) UnusedRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL", userID).Scan(&n)
	return n, err
}

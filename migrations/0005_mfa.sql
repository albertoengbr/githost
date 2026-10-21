-- 0005_mfa: two-factor authentication (TOTP) and recovery codes.
--
-- TOTP secrets are stored encrypted-at-rest by the application (the
-- instance secret signs them); recovery codes are hash-only.

ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN mfa_enabled INTEGER NOT NULL DEFAULT 0;

-- Sessions issued after a password check but before the TOTP challenge
-- are flagged here; requireSession refuses every page while the flag is
-- set, so a stolen/forwarded pre-MFA cookie grants nothing.
ALTER TABLE sessions ADD COLUMN mfa_pending INTEGER NOT NULL DEFAULT 0;

CREATE TABLE recovery_codes (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	code_hash  TEXT NOT NULL UNIQUE,
	used_at    TEXT,
	created_at TEXT NOT NULL
);
CREATE INDEX idx_recovery_codes_user ON recovery_codes(user_id);

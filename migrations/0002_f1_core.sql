-- 0002_f1_core: accounts, credentials, repositories, permissions, audit.
--
-- Introduced by F1 (Git core). On-disk bare repositories are addressed by
-- repositories.uuid (see internal/storage); owner/name only exists as
-- metadata resolved through this schema, never as a filesystem path.

CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	email         TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL DEFAULT '',
	is_admin      INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE ssh_keys (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	fingerprint TEXT NOT NULL UNIQUE,
	public_key  TEXT NOT NULL,
	title       TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);
CREATE INDEX idx_ssh_keys_user ON ssh_keys(user_id);

CREATE TABLE access_tokens (
	id           INTEGER PRIMARY KEY,
	user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT NOT NULL DEFAULT '',
	token_hash   TEXT NOT NULL UNIQUE,
	token_prefix TEXT NOT NULL DEFAULT '',
	scopes       TEXT NOT NULL DEFAULT '',
	expires_at   TEXT,
	revoked_at   TEXT,
	created_at   TEXT NOT NULL
);
CREATE INDEX idx_access_tokens_user ON access_tokens(user_id);

CREATE TABLE repositories (
	id             INTEGER PRIMARY KEY,
	uuid           TEXT NOT NULL UNIQUE,
	owner_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name           TEXT NOT NULL,
	description    TEXT NOT NULL DEFAULT '',
	visibility     TEXT NOT NULL DEFAULT 'private'
		CHECK (visibility IN ('private', 'public')),
	default_branch TEXT NOT NULL DEFAULT 'main',
	is_archived    INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL,
	UNIQUE (owner_id, name)
);

CREATE TABLE repository_members (
	repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	permission    TEXT NOT NULL
		CHECK (permission IN ('read', 'write', 'admin')),
	created_at    TEXT NOT NULL,
	PRIMARY KEY (repository_id, user_id)
);

CREATE TABLE audit_events (
	id         INTEGER PRIMARY KEY,
	actor_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX idx_audit_events_time ON audit_events(created_at);

-- Sessions are reserved for the web UI (F2); created now so the schema
-- is stable across that phase.
CREATE TABLE sessions (
	id         TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	ip         TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
);

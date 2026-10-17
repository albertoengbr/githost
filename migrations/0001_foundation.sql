-- 0001_foundation: GitHost baseline schema.
--
-- F0 establishes the migration machinery plus a small instance metadata
-- table. Domain tables (users, repositories, issues, ...) are added by
-- later, phase-gated migrations; see docs/architecture/database.md for the
-- planned full schema.

CREATE TABLE app_meta (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

INSERT INTO app_meta (key, value, updated_at)
VALUES ('schema', '0001-foundation', 'static')
ON CONFLICT (key) DO NOTHING;

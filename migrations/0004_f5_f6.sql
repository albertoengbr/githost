-- 0004_f5_f6: webhooks, event log, scheduled jobs, search index,
-- package registry.

CREATE TABLE webhooks (
	id            INTEGER PRIMARY KEY,
	repository_id  INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	url            TEXT NOT NULL,
	secret         TEXT NOT NULL DEFAULT '',
	events         TEXT NOT NULL DEFAULT 'push', -- comma-separated: push,issues,pulls
	active         INTEGER NOT NULL DEFAULT 1,
	created_at     TEXT NOT NULL
);

CREATE TABLE webhook_deliveries (
	id          INTEGER PRIMARY KEY,
	webhook_id   INTEGER NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
	event       TEXT NOT NULL,
	payload     TEXT NOT NULL,
	status_code INTEGER NOT NULL DEFAULT 0,
	success     INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL
);
CREATE INDEX idx_webhook_deliveries ON webhook_deliveries(webhook_id, created_at DESC);

CREATE TABLE jobs (
	id          INTEGER PRIMARY KEY,
	kind        TEXT NOT NULL,  -- prune_sessions | rotate_backups | webhook_retries
	status      TEXT NOT NULL DEFAULT 'pending', -- pending | running | done | failed
	detail      TEXT NOT NULL DEFAULT '',
	run_at      TEXT NOT NULL,
	finished_at  TEXT,
	created_at   TEXT NOT NULL
);
CREATE INDEX idx_jobs_pending ON jobs(status, run_at);

CREATE TABLE packages (
	id            INTEGER PRIMARY KEY,
	repository_id  INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	version       TEXT NOT NULL,
	kind          TEXT NOT NULL DEFAULT 'generic', -- generic | archive
	filename      TEXT NOT NULL,
	size          INTEGER NOT NULL,
	sha256        TEXT NOT NULL,
	storage_path  TEXT NOT NULL,
	metadata      TEXT NOT NULL DEFAULT '',
	author_id     INTEGER NOT NULL REFERENCES users(id),
	created_at    TEXT NOT NULL,
	UNIQUE (repository_id, name, version)
);

-- 0003_f4_collab: issues, pull requests, reviews, releases, branch rules.
--
-- PRs are same-repository (base/head are branch names in the same bare
-- repo); forks are out of scope for F4. Markdown content is stored raw
-- and rendered safely at display time (internal/markdown).

CREATE TABLE issues (
	id           INTEGER PRIMARY KEY,
	repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	number       INTEGER NOT NULL,
	title        TEXT NOT NULL,
	body_md      TEXT NOT NULL DEFAULT '',
	state        TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
	author_id    INTEGER NOT NULL REFERENCES users(id),
	assignee_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	UNIQUE (repository_id, number)
);

CREATE TABLE issue_comments (
	id         INTEGER PRIMARY KEY,
	issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	author_id  INTEGER NOT NULL REFERENCES users(id),
	body_md    TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX idx_issue_comments_issue ON issue_comments(issue_id);

CREATE TABLE labels (
	id            INTEGER PRIMARY KEY,
	repository_id  INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	color         TEXT NOT NULL DEFAULT '666666',
	created_at    TEXT NOT NULL,
	UNIQUE (repository_id, name)
);

CREATE TABLE issue_labels (
	issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
	PRIMARY KEY (issue_id, label_id)
);

-- issue_events records the change history an issue page displays.
CREATE TABLE issue_events (
	id         INTEGER PRIMARY KEY,
	issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	actor_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	kind       TEXT NOT NULL, -- created | closed | reopened | labeled | unlabeled | assigned | unassigned
	detail     TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX idx_issue_events_issue ON issue_events(issue_id);

CREATE TABLE pull_requests (
	id           INTEGER PRIMARY KEY,
	repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	number       INTEGER NOT NULL,
	title        TEXT NOT NULL,
	body_md      TEXT NOT NULL DEFAULT '',
	base_branch  TEXT NOT NULL,
	head_branch  TEXT NOT NULL,
	state         TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'merged', 'closed')),
	merge_commit  TEXT,
	merged_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
	author_id     INTEGER NOT NULL REFERENCES users(id),
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	UNIQUE (repository_id, number)
);

CREATE TABLE pull_request_reviews (
	id         INTEGER PRIMARY KEY,
	pull_id    INTEGER NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
	reviewer_id INTEGER NOT NULL REFERENCES users(id),
	verdict    TEXT NOT NULL CHECK (verdict IN ('approve', 'reject', 'comment')),
	body_md    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX idx_pr_reviews_pull ON pull_request_reviews(pull_id);

CREATE TABLE pull_request_comments (
	id         INTEGER PRIMARY KEY,
	pull_id    INTEGER NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
	author_id  INTEGER NOT NULL REFERENCES users(id),
	body_md    TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX idx_pr_comments_pull ON pull_request_comments(pull_id);

CREATE TABLE releases (
	id            INTEGER PRIMARY KEY,
	repository_id  INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	tag_name       TEXT NOT NULL,
	name           TEXT NOT NULL DEFAULT '',
	notes_md       TEXT NOT NULL DEFAULT '',
	author_id      INTEGER NOT NULL REFERENCES users(id),
	is_draft       INTEGER NOT NULL DEFAULT 0,
	published_at   TEXT,
	created_at     TEXT NOT NULL,
	UNIQUE (repository_id, tag_name)
);

CREATE TABLE release_assets (
	id          INTEGER PRIMARY KEY,
	release_id   INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
	filename     TEXT NOT NULL,
	size         INTEGER NOT NULL,
	sha256       TEXT NOT NULL,
	storage_path TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	UNIQUE (release_id, filename)
);

-- branch_rules enforce protections on push (installed pre-receive
-- hook) and on PR merges (required approvals).
CREATE TABLE branch_rules (
	id             INTEGER PRIMARY KEY,
	repository_id   INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	pattern         TEXT NOT NULL, -- glob matched against branch short name
	require_approval INTEGER NOT NULL DEFAULT 0,
	block_force_push INTEGER NOT NULL DEFAULT 0,
	block_deletion   INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL
);
CREATE INDEX idx_branch_rules_repo ON branch_rules(repository_id);

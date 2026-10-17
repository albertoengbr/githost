# Database design

## Store split

- **SQLite** (`<data>/githost.db`): accounts, permissions, issues, pull
  requests, releases, jobs, audit events — everything *about* repositories.
- **Plain Git repositories** (`<data>/repositories/<id>.git`): the actual
  versioned content, in 100% standard Git format, operated only by the
  official git binary. Never converted, never parsed into SQL.

This split is a hard architectural line: it keeps history portable (a
`git clone` of the data directory is a valid migration path), keeps the
database small, and lets `git fsck` be the integrity authority.

## Connection policy (implemented in F0)

- Driver: `modernc.org/sqlite` (pure Go — ADR-0001).
- DSN pragmas applied to **every** pooled connection: `journal_mode=WAL`,
  `busy_timeout=5000`, `synchronous=NORMAL`, `foreign_keys=ON`.
- Pool: max 4 open connections (concurrent readers under WAL, serialized
  writers with busy timeout).
- The parent directory of the database file is created on open.

## Migration policy (implemented in F0)

- Files: `migrations/NNNN_name.sql`, embedded in the binary via
  `go:embed`; applied by the runner in `internal/database`.
- Each migration runs **inside a transaction**; failure rolls back
  statement-by-statement work and records nothing.
- Applied versions are recorded in `schema_migrations(version, name,
  applied_at)`.
- Re-running `Migrate` is a no-op; running it on every `serve` keeps
  instances current (idempotent).
- A database holding versions absent from the running binary is
  **dirty** (downgrade scenario) and migration refuses to proceed.
- Only forward migrations ship. Rollback = restore from backup (F3).

Current schema state:

- `0001_foundation.sql` — `app_meta(key, value, updated_at)` baseline
  metadata table + `schema_migrations` (created by the runner).

## Entity model (planned; phase-gated migrations)

Entities from the project charter, grouped by phase. Column sketches
below are the design reference for future migrations — each phase adds
its tables through a new `NNNN_*.sql` and this file is updated to match
what shipped.

| Entity | Purpose | Key columns (sketch) | Phase |
|---|---|---|---|
| users | accounts | id, username (unique), email, argon2id_hash, is_admin, created_at | F1 |
| sessions | web sessions | id (token hash), user_id, expires_at, created_at, ip, agent | F1 |
| ssh_keys | public keys | id, user_id, fingerprint (unique), public_key, title, added_at | F1 |
| access_tokens | API/Git tokens | id, user_id, name, token_hash, scopes, expires_at, revoked_at | F1 |
| repositories | repo registry | id (internal UUID), owner_id, name, description, visibility (default private), default_branch, is_archived, created_at | F1 |
| repository_members | collaborators | repo_id, user_id, permission (read/write/admin) | F1 |
| audit_events | security log | id, actor_id, action, target, detail, created_at | F1 |
| branch_rules | protection | repo_id, pattern, require_review, block_force_push | F4 |
| issues | issue tracker | id, repo_id, number, title, body_md, state, author_id, assignee_id, created_at | F4 |
| issue_comments | discussion | id, issue_id, author_id, body_md, created_at | F4 |
| labels | issue labels | id, repo_id, name, color | F4 |
| pull_requests | code review | id, repo_id, number, title, base_ref, head_ref, state, merge_commit, author_id | F4 |
| pull_request_reviews | reviews | id, pr_id, reviewer_id, verdict (approve/reject/comment), body_md, created_at | F4 |
| releases | versioned releases | id, repo_id, tag_name, name, notes_md, author_id, published_at | F4 |
| release_assets | artifacts | id, release_id, filename, size, sha256, storage_path | F4 |
| webhooks | outbound hooks | id, repo_id, url, secret, events, active | F5 |
| jobs | background work | id, kind, payload, status, run_at, finished_at | F5 |

Design rules for every migration:

- UUIDs (as TEXT) for external IDs; INTEGER PRIMARY KEY only where a
  natural monotonically increasing key is genuinely right.
- `username`/`repository name` uniqueness enforced at DB level, not just
  application level.
- Referential integrity via FOREIGN KEYs (enforced by the connection
  pragma, not by convention).
- Timestamps as RFC 3339 TEXT (SQLite-idiomatic, sortable).
- No denormalization until a measured need exists.

## Backups (F3 design note)

A consistent backup requires coordinating two stores. Plan: pause writes
via an application-level lock (single process makes this tractable),
`VACUUM INTO`/snapshot the SQLite file (or use the SQLite backup API),
copy `repositories/` with content hashing, write a manifest with
SHA-256 of every artifact, and verify the manifest on restore. The
acceptance criterion is a working instance restored into a clean install,
not the existence of archive files.

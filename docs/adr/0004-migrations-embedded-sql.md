# ADR-0004: Embedded, versioned SQL migrations with a minimal runner

- Status: ACCEPTED
- Date: 2026-10-17

## Context

SQLite schema evolves with each phase (F0: machinery; F1: users/repos;
F4: issues/PRs...). The charter requires versioned SQL migrations. Off
-the-shelf runners (golang-migrate, goose) are capable but add
dependencies and CLI surface for a single-database application whose
migration needs are linear and forward-only.

## Decision

Ship migrations as `migrations/NNNN_name.sql` embedded via `go:embed`,
applied by a ~150-line runner in `internal/database`:

- Files are the single source of schema truth; naming is strict
  (`0001_foundation.sql`), duplicates and malformed names are errors.
- Each migration executes inside a transaction: statement failure rolls
  the whole migration back and nothing is recorded.
- Applied versions live in `schema_migrations(version, name, applied_at)`.
- `Migrate` runs at every `init`/`serve`/test start — idempotent by
  design.
- A database containing versions unknown to the running binary is
  **dirty** (downgraded instance) and migration refuses to continue
  rather than guess.
- A small SQL statement splitter (understands strings, quoted
  identifiers, `--` and `/* */` comments) keeps migration files
  readable multi-statement documents; it is unit-tested.

## Consequences

- No migration-framework dependency; behavior fully owned and tested
  (apply, idempotency, rollback, dirty-detection all have tests).
- No down-migrations: rollback strategy is "restore from backup" (F3
  deliverable), consistent with the single-binary, single-database
  scope.
- Third-party runners remain an upgrade path if needs grow (e.g.
  concurrent instances, PostgreSQL support in F6+), which would get
  its own ADR.

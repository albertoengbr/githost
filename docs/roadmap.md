# GitHost roadmap

Phases follow dependency order; a phase is only complete when its
acceptance criteria pass with recorded evidence (docs/status.md).

| Phase | Theme | State |
|---|---|---|
| F0 | Foundation | **ACCEPTED** (see docs/testing/f0-evidence.md) |
| F1 | Git core (repositories, Smart HTTP, SSH, auth) | SPECIFIED — next |
| F2 | Web UI + REST API | SPECIFIED (high level) |
| F3 | Stable MVP: backup/restore, E2E, packaging, multiplatform validation | SPECIFIED (high level) |
| F4 | Collaboration: issues, PRs, reviews, releases, wiki, LFS, MFA | SPECIFIED (high level) |
| F5 | Integrations: webhooks, import/mirroring, schedulers, CI runners | NOT_STARTED |
| F6 | Expansion: package registry, advanced search, extensions | NOT_STARTED |

## F0 — Foundation (ACCEPTED)

Delivered: research docs, requirements spec, architecture docs, threat
model, Go skeleton, YAML config with validation, structured logging,
SQLite (pure-Go driver) with embedded transactional migrations, CLI
(init/serve/version/doctor), HTTP server with /healthz and /version,
unit+integration tests, verification scripts, cross-compilation matrix.

Acceptance (all met — evidence: docs/testing/f0-evidence.md):
1. Application starts and serves; configuration loads and validates.
2. Database initialized and migrated (idempotent, dirty-safe).
3. `go vet`, `gofmt`, `go test` green on native Windows.
4. Full 6-OS × 2-arch cross-compile matrix attempted; 11/12 build, 1
   documented as unsupported (netbsd/arm64 — dependency limitation).
5. Documentation set consistent with implementation.

## F1 — Git core (SPECIFIED)

Scope: git executor (allowlist, env control, timeouts), bare repository
store by internal ID, Smart HTTP transport, restricted SSH transport,
Argon2id auth + sessions + scoped tokens, per-repo authorization, E2E
proof with real `git clone/push/pull` over both transports.

Acceptance gate: a repository created in GitHost can be cloned over
HTTPS and SSH; commits can be pushed and pulled; history is byte-intact
(`git fsck` clean); unauthorized access is denied (verified negative
tests); private repos invisible to anonymous users.

## F2 — Web UI + API (SPECIFIED, high level)

Dashboard, repository browser (files/commits/diffs/branches/tags),
user/settings pages, REST API `/api/v1` with OpenAPI contract. All pages
driven by real data; no static mockups.

## F3 — Stable MVP

Backup/restore (functional restore against clean install is the
criterion), security hardening pass, E2E suite, operational
documentation, per-platform packaging with checksums, Docker Linux
image, validated platform matrix.

## F4 — Collaboration

Issues, pull requests with reviews and merges, branch protection,
releases with assets, wiki, Git LFS (only after full protocol testing),
MFA.

## F5 — Integrations

Webhooks, import/mirroring of existing repositories, scheduled jobs,
isolated CI runners.

## F6 — Expansion

Package registry, advanced search, remote storage backends, extensions.

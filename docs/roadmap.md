# GitHost roadmap

Phases follow dependency order; a phase is only complete when its
acceptance criteria pass with recorded evidence (docs/status.md).

| Phase | Theme | State |
|---|---|---|
| F0 | Foundation | **ACCEPTED** (see docs/testing/f0-evidence.md) |
| F1 | Git core (repositories, Smart HTTP, SSH, auth) | **ACCEPTED** (see docs/testing/f1-evidence.md) |
| F2 | Web UI + REST API | SPECIFIED (high level) — next |
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

## F1 — Git core (ACCEPTED)

Delivered and verified with real git/ssh clients (evidence:
docs/testing/f1-evidence.md): git executor with subcommand allowlist and
environment jail; UUID-addressed bare repository storage; users with
Argon2id passwords; access tokens with scopes (hash-only storage);
SSH key registry; per-repository authorization; CLI admin/repo
commands; Git Smart HTTP (protocol v0 and v2) with token auth; restricted
SSH transport (public-key only, exec-only, no shell/PTY/forwarding);
audit events; E2E clone/commit/push/pull over both transports with
`git fsck --strict` integrity checks and cross-transport pull.

Acceptance gate (all met): repository created in GitHost, cloned over
HTTPS and SSH, commits pushed and pulled, history byte-intact, every
authorization boundary verified by negative tests, live binary demo
over both transports.

## F2 — Web UI + API (ACCEPTED)

Delivered and verified (evidence: docs/testing/f2-evidence.md): session
login (Argon2id verification, hardened cookies, CSRF double-submit bound
to sessions, rate-limited guesses), a server-rendered UI with embedded
templates/assets and zero JavaScript (dashboard, repository list,
creation form, repository home with README preview, tree browser, file
viewer, raw download, paginated commits, commit detail with patch,
branches, tags, profiles, SSH key and token settings, repository
settings incl. rename/archive/delete), and the versioned REST API
`/api/v1` (10 endpoints, token scopes, pagination, structured errors)
with an OpenAPI contract guarded by a drift test.

Acceptance (all met): every screen renders real backend data; private
repositories are invisible to unauthorized viewers everywhere; the API
contract cannot drift silently.

## F3 — Stable MVP (ACCEPTED)

Delivered and verified (evidence: docs/testing/f3-evidence.md): OS-level
instance lock coordinating serve/backup/restore; backup as a
manifest-plus-SHA-256 tar.gz (VACUUM INTO snapshot); validation that
rejects tampered archives; restore restricted to clean installs with
retention/prune; the **restore gate** — a backed-up instance restored
into a clean install proves functional (login, clone with the old
token, push, web rendering, fsck clean); native TLS serving; push size
limits enforced by git itself plus the HTTP layer; packaging (11
archives + SHA256SUMS, Docker image built and smoke tested on Linux,
hardened systemd unit, launchd plist, FreeBSD rc.d, Windows wrapper
guide); CI workflow with POSIX race testing.

Acceptance (all met): recovery is **functional**, not just archived;
no known critical vulnerabilities in the implemented flows; the
platform story advanced (linux/amd64 container-validated) without
overclaiming support.

## F4 — Collaboration (ACCEPTED)

Delivered and verified (evidence: docs/testing/f4-evidence.md): safe
Markdown rendering (goldmark with XSS corpus tests — raw HTML omitted,
dangerous link schemes stripped); issues with Markdown bodies, comments,
close/reopen, labels, assignees and full event history; pull requests
with commit listing, diff display and conflict detection (merge-tree
--write-tree); reviews with approve/reject/comment (latest verdict per
reviewer wins); three merge strategies (merge-commit, squash,
fast-forward-only) executed through git plumbing (commit-tree +
update-ref with CAS); branch protection rules with glob patterns and
approval gates enforced at the single merge entry point. The
collaboration e2e gate proves the full workflow: issue → PR →
review → protection → merge → clean fsck.

Releases with assets, Git-backed Wiki, Git LFS and MFA are deferred to
F5/F6 (recorded in status.md).

## F5 — Integrations (ACCEPTED)

Delivered and verified (evidence: docs/testing/f5f6-evidence.md): the
internal event bus (in-process, synchronous, error-isolated) as the
backbone for outbound integrations; webhooks with HMAC-SHA256
signatures, per-event filtering (push/issues/pulls), delivery records
with status codes, and URL scheme validation (http/https only);
repository import from remote URLs (git clone --mirror + push --mirror,
with compensating delete on failure); and scheduled jobs using
SQLite-as-queue with CAS claiming (prune_sessions, rotate_backups).

## F6 — Expansion (ACCEPTED)

Delivered and verified (evidence: docs/testing/f5f6-evidence.md):
search across issues and repositories (LIKE-based, adequate for
personal scale); a package registry with sha256 integrity, size caps
(100 MiB), and per-repository versioning; and extension points — new
event kinds and job kinds require only a constant addition to the
respective packages.

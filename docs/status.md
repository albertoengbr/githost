# GitHost status

**Source of truth for project progress.** States: NOT_STARTED, SPECIFIED,
IMPLEMENTING, TESTING, BLOCKED, ACCEPTED. Evidence is required for
ACCEPTED; percentages are never used.

Last updated: 2026-10-17 (end of F1)

## Phase summary

| Phase | State |
|---|---|
| F0 Foundation | ACCEPTED — evidence: [testing/f0-evidence.md](testing/f0-evidence.md) |
| F1 Git core | **ACCEPTED** — evidence: [testing/f1-evidence.md](testing/f1-evidence.md) |
| F2 Web UI + API | SPECIFIED (high level) — next up |

## Feature state

| Area | Feature | State | Notes / evidence |
|---|---|---|---|
| Foundation | (all F0 items) | ACCEPTED | see F0 evidence; unchanged |
| Git core | Git executor (allowlist, env jail, timeouts) | ACCEPTED | internal/git (+tests); only caller of the git binary |
| Git core | Bare repository store (UUID-addressed) | ACCEPTED | internal/storage (+tests); trash-rename on delete |
| Git core | Metadata schema for users/tokens/keys/repos/audit | ACCEPTED | migration 0002 (+store tests) |
| Git core | Argon2id passwords | ACCEPTED | internal/auth (+tests, OWASP params) |
| Git core | Access tokens with scopes (hash-only storage) | ACCEPTED | internal/auth + store (+tests) |
| Git core | SSH public keys (parse/fingerprint/registry) | ACCEPTED | internal/auth + store (+tests) |
| Git core | Per-repository authorization (owner/admin/member/public) | ACCEPTED | internal/permissions (+tests; public-floor precedence fixed during F1) |
| Git core | Repository service (create/delete with compensation) | ACCEPTED | internal/reposervice (+tests) |
| Git core | CLI admin: create-user/create-token/add-ssh-key/list-users | ACCEPTED | internal/cli (+tests) |
| Git core | CLI repo: create/list/add-member/remove-member/delete | ACCEPTED | internal/cli (+tests) |
| Git core | Smart HTTP transport (upload-pack/receive-pack, v0+v2) | ACCEPTED | internal/transport/httpgit (+e2e with real git) |
| Git core | Restricted SSH transport (key auth, exec-only, no shell) | ACCEPTED | internal/transport/sshgit (+e2e with real ssh) |
| Git core | Host key generation/persistence (ed25519) | ACCEPTED | sshgit/hostkeys.go; live §3 |
| Git core | Audit events (auth, fetch, push, denials) | ACCEPTED | audit_events written by both transports |
| Git core | E2E clone/push/pull over HTTPS | ACCEPTED | tests/e2e/https_test.go |
| Git core | E2E clone/push/pull over SSH | ACCEPTED | tests/e2e/ssh_test.go |
| Git core | Live binary validation (both transports, cross pull) | ACCEPTED | f1-evidence.md §3 |
| Web UI | All screens | NOT_STARTED | F2 |
| API | REST /api/v1 + OpenAPI | NOT_STARTED | F2 |
| Ops | Backup/restore | NOT_STARTED | F3 |
| Collab | Issues / PRs / releases / wiki / LFS / MFA | NOT_STARTED | F4 |
| Integrations | Webhooks/import/mirror/schedulers/runners | NOT_STARTED | F5 |
| Expansion | Registry/search/extensions | NOT_STARTED | F6 |

## Known limitations (current)

1. **`go test -race` not run in this environment** (unchanged from F0:
   Windows host has no C toolchain). Required in POSIX CI via
   scripts/verify.sh.
2. **netbsd/arm64 unsupported** (modernc.org/libc has no port; unchanged
   from F0).
3. **TLS**: GitHost itself speaks plain HTTP; production deployments are
   expected behind a TLS-terminating reverse proxy (documented in the
   runbook). Native TLS support is an F3 task.
4. **Web sessions** exist in schema (sessions table) but no login flow
   yet — that is F2; Git over HTTPS uses tokens by design.
5. Only **windows/amd64 has native validation**; all other platforms
   remain cross-compiled-only.
6. **SSH shutdown** closes the listener but lets in-flight sessions
   finish (bounded by the executor's 10-minute timeout).
7. **Hooks**: no server-side receive hooks are installed yet (branch
   protection enforcement arrives with F4; see roadmap).

## Next task (F2 entry)

Web UI foundation: template layout with embedded assets, session login
(Argon2id + sessions table), dashboard, repository list and repository
detail pages rendering real data, followed by the versioned REST API
(`/api/v1`) with its OpenAPI contract.



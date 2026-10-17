# GitHost status

**Source of truth for project progress.** States: NOT_STARTED, SPECIFIED,
IMPLEMENTING, TESTING, BLOCKED, ACCEPTED. Evidence is required for
ACCEPTED; percentages are never used.

Last updated: 2026-10-17 (end of F2)

## Phase summary

| Phase | State |
|---|---|
| F0 Foundation | ACCEPTED — evidence: [testing/f0-evidence.md](testing/f0-evidence.md) |
| F1 Git core | ACCEPTED — evidence: [testing/f1-evidence.md](testing/f1-evidence.md) |
| F2 Web UI + API | **ACCEPTED** — evidence: [testing/f2-evidence.md](testing/f2-evidence.md) |
| F3 Stable MVP (backup/restore, packaging, platform validation) | SPECIFIED (high level) — next up |
| F4 Collaboration | SPECIFIED (high level) |
| F5 Integrations | NOT_STARTED |
| F6 Expansion | NOT_STARTED |

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
| Web UI | Session login (Argon2id, cookies, CSRF, rate-limited) | ACCEPTED | internal/session+csrf+ratelimit (+tests); web login flow |
| Web UI | Dashboard, repository list, creation form | ACCEPTED | internal/web (+tests, live §4 f2-evidence) |
| Web UI | Repository home with README preview | ACCEPTED | renders real git content (plain `<pre>`; Markdown rendering deferred) |
| Web UI | Tree browser, file viewer, raw download | ACCEPTED | internal/web (+tests) |
| Web UI | Commit history (paginated), commit detail + patch | ACCEPTED | internal/web (+tests) |
| Web UI | Branches and tags pages | ACCEPTED | internal/web (+tests) |
| Web UI | User profile pages | ACCEPTED | internal/web |
| Web UI | SSH keys settings page | ACCEPTED | add/remove with CSRF |
| Web UI | Access tokens settings page (reveal-once) | ACCEPTED | plaintext shown exactly once |
| Web UI | Repository settings (visibility/default branch/rename/archive/delete) | ACCEPTED | owner/admin only; 404 for others on private |
| Web UI | Light/dark themes, zero JavaScript, embedded assets | ACCEPTED | single CSS via prefers-color-scheme |
| API | REST /api/v1 (10 endpoints, tokens+scopes, pagination) | ACCEPTED | internal/api (+tests) |
| API | OpenAPI contract + sync test | ACCEPTED | docs/api/openapi.yaml; drift fails the test |
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
   runbook). Native TLS support is an F3 task. Session cookies therefore
   set `Secure` only when the connection is direct TLS; behind a proxy
   the operator must ensure cookie transport security.
4. **README rendering** is plain text (`<pre>`); Markdown rendering and
   syntax highlighting are deferred (with Issues/PRs in F4, where a
   Markdown renderer becomes mandatory).
5. **i18n**: UI is English-only; the locales structure arrives with the
   first translated strings (spec §12 remains on the roadmap).
6. Only **windows/amd64 has native validation**; all other platforms
   remain cross-compiled-only.
7. **SSH shutdown** closes the listener but lets in-flight sessions
   finish (bounded by the executor's 10-minute timeout).
8. **Hooks**: no server-side receive hooks are installed yet (branch
   protection enforcement arrives with F4; see roadmap).
9. **API surface**: v1 covers users/repos/refs/commits; issues and pull
   requests endpoints arrive with F4 (spec §15 lists them under the same
   /api/v1 prefix).
10. **Password change / MFA**: no self-service password change yet
    (operator-managed via CLI); MFA is F4 scope.

## Next task (F3 entry)

Stable MVP hardening: backup and restore with manifest + hashes and a
restore-against-clean-install acceptance test, native TLS option,
packaging (per-OS archives with checksums, Docker Linux image, service
manager unit files), payload size limits on Git pushes, and CI wiring
(race detector on POSIX, cross-compile matrix, native validation runs).



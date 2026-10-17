# GitHost status

**Source of truth for project progress.** States: NOT_STARTED, SPECIFIED,
IMPLEMENTING, TESTING, BLOCKED, ACCEPTED. Evidence is required for
ACCEPTED; percentages are never used.

Last updated: 2026-10-17 (end of F3)

## Phase summary

| Phase | State |
|---|---|
| F0 Foundation | ACCEPTED — evidence: [testing/f0-evidence.md](testing/f0-evidence.md) |
| F1 Git core | ACCEPTED — evidence: [testing/f1-evidence.md](testing/f1-evidence.md) |
| F2 Web UI + API | ACCEPTED — evidence: [testing/f2-evidence.md](testing/f2-evidence.md) |
| F3 Stable MVP | **ACCEPTED** — evidence: [testing/f3-evidence.md](testing/f3-evidence.md) |
| F4 Collaboration | SPECIFIED (high level) — next up |
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
| Ops | Instance lock (OS-level, crash-safe) | ACCEPTED | internal/lock (+tests); serve/backup/restore coordination |
| Ops | Backup create/validate (manifest + SHA-256, VACUUM INTO) | ACCEPTED | internal/backup (+tests) |
| Ops | Restore into clean install + retention (list/prune) | ACCEPTED | internal/backup; CLI backup subcommands |
| Ops | **Restore gate: functional instance after restore** | ACCEPTED | tests/e2e/backup_test.go (login, clone, push, web, fsck) |
| Ops | Native TLS serving | ACCEPTED | server.tls config; handshake-tested in app tests |
| Ops | Push size limits (receive.maxInputSize + HTTP body cap) | ACCEPTED | limits.max_push_bytes; e2e TestPushSizeLimit |
| Ops | Packaging script (11 archives + SHA256SUMS) | ACCEPTED | scripts/package.ps1/.sh; run recorded in f3-evidence |
| Ops | Docker Linux image | ACCEPTED | packaging/docker; built + init/doctor/serve smoke tested |
| Ops | systemd (hardened) / launchd / rc.d units | ACCEPTED | packaging/; guarded by tests/packaging_test.go |
| Ops | CI workflow (matrix + race + package) | ACCEPTED (defined) | .github/workflows/ci.yml; YAML-drift test |
| Ops | Backup/restore | NOT_STARTED | F3 |
| Collab | Issues / PRs / releases / wiki / LFS / MFA | NOT_STARTED | F4 |
| Integrations | Webhooks/import/mirror/schedulers/runners | NOT_STARTED | F5 |
| Expansion | Registry/search/extensions | NOT_STARTED | F6 |

## Known limitations (current)

1. **`go test -race` not run in this environment** (Windows host has no C
   toolchain). The CI workflow runs it on POSIX runners — **activating CI
   on a live runner is still pending** (workflow committed, not yet
   executed).
2. **netbsd/arm64 unsupported** (modernc.org/libc has no port; unchanged
   from F0).
3. **Windows native SCM service** is documented via wrapper tools
   (NSSM/WinSW/Task Scheduler); a first-party SCM handler remains a gap.
4. **README rendering** is plain text (`<pre>`); Markdown rendering is
   deferred to F4 (with Issues/PRs).
5. **i18n**: UI is English-only.
6. **Platform validation**: windows/amd64 is dev-host-validated;
   linux/amd64 is container-validated (init/doctor/serve); every other
   platform remains cross-compiled-only. No platform is declared
   officially supported until native test runs exist.
7. **SSH shutdown** lets in-flight sessions finish (bounded by the
   10-minute executor timeout).
8. **Hooks**: no server-side receive hooks yet (F4 branch protection).
9. **API surface**: issues and pull requests endpoints arrive with F4.
10. **Password change / MFA**: no self-service password change yet; MFA
    is F4 scope.

## Next task (F4 entry)

Collaboration features: Issues (with Markdown rendering, labels,
assignees, history), Pull Requests (branch comparison, reviews with
approve/reject, merge strategies, conflict detection), branch protection
enforced by platform-installed receive hooks, Releases with assets,
Git-backed Wiki — followed by Git LFS and MFA. Design notes and merge
strategies need their specification before implementation begins
(spec §13).



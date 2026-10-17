# GitHost status

**Source of truth for project progress.** States: NOT_STARTED, SPECIFIED,
IMPLEMENTING, TESTING, BLOCKED, ACCEPTED. Evidence is required for
ACCEPTED; percentages are never used.

Last updated: 2026-10-17 (end of F0)

## Phase summary

| Phase | State |
|---|---|
| F0 Foundation | ACCEPTED — evidence: [testing/f0-evidence.md](testing/f0-evidence.md) |
| F1 Git core | SPECIFIED — next up |

## Feature state

| Area | Feature | State | Notes / evidence |
|---|---|---|---|
| Foundation | Requirements specification | ACCEPTED | docs/requirements.md v1.0 |
| Foundation | Reference research (Gogs/Gitea/Forgejo/Git protocols) | ACCEPTED | docs/research/ |
| Foundation | Architecture documentation | ACCEPTED | docs/architecture/ |
| Foundation | Threat model | ACCEPTED | docs/security/threat-model.md |
| Foundation | Project skeleton + module | ACCEPTED | go.mod, cmd/, internal/ |
| Foundation | YAML config load/validate/resolve | ACCEPTED | internal/config (+tests) |
| Foundation | Structured logging | ACCEPTED | internal/logging (+tests) |
| Foundation | Instance secret management | ACCEPTED | internal/security (+tests) |
| Foundation | SQLite open with pragmas (WAL, busy timeout, FK) | ACCEPTED | internal/database (+tests) |
| Foundation | Embedded versioned migrations (transactional, dirty-safe) | ACCEPTED | internal/database/migrate.go, migrations/ (+tests) |
| Foundation | CLI init/serve/version/doctor | ACCEPTED | internal/cli (+tests, live smoke) |
| Foundation | HTTP server /healthz /version + middleware | ACCEPTED | internal/httpserver (+tests, live smoke) |
| Foundation | Graceful shutdown | ACCEPTED | internal/app (+test), serve signal handling |
| Foundation | Verification scripts + cross-compile matrix | ACCEPTED | scripts/, 11/12 targets (netbsd/arm64 unsupported — see below) |
| Foundation | Documentation set | ACCEPTED | docs/ |
| Git core | Git executor (allowlist, env control, limits) | SPECIFIED | design in git-protocols.md + threat model; F1 |
| Git core | Bare repository store (ID-addressed) | SPECIFIED | F1 |
| Git core | Smart HTTP transport | SPECIFIED | protocol notes ready; F1 |
| Git core | Restricted SSH transport | SPECIFIED | protocol notes ready; F1 |
| Git core | Users/sessions/tokens (Argon2id, scopes) | SPECIFIED | F1 |
| Git core | Per-repo authorization | SPECIFIED | F1 |
| Git core | E2E clone/push/pull proof | NOT_STARTED | F1 acceptance gate |
| Web UI | All screens (dashboard, browser, commits, settings...) | NOT_STARTED | F2 |
| API | REST /api/v1 + OpenAPI | NOT_STARTED | F2 |
| Ops | Backup/restore | NOT_STARTED | F3 |
| Collab | Issues | NOT_STARTED | F4 |
| Collab | Pull requests/reviews/merge | NOT_STARTED | F4 |
| Collab | Releases/assets | NOT_STARTED | F4 |
| Collab | Wiki | NOT_STARTED | F4 |
| Collab | Git LFS | NOT_STARTED | F4 (only after full protocol tests) |
| Collab | MFA | NOT_STARTED | F4 |
| Integrations | Webhooks/import/mirror/schedulers/runners | NOT_STARTED | F5 |
| Expansion | Registry/search/extensions | NOT_STARTED | F6 |

## Known limitations (current)

1. **`go test -race` not run in the F0 environment**: the Windows dev
   host has no C toolchain (gcc), which `-race` requires there. Race
   testing is part of `scripts/verify.sh` (POSIX) and must run in CI.
   Tracked as a testing gap, not as passing.
2. **netbsd/arm64 is not supported** by the pure-Go SQLite stack
   (modernc.org/libc has no netbsd/arm64 port as of v1.77.1). All other
   11 matrix targets cross-compile. See
   [compatibility/platform-matrix.md](compatibility/platform-matrix.md).
3. **PostgreSQL** is architecture-planned but intentionally not
   implemented (stabilize SQLite core first).
4. Only **windows/amd64 has native validation** so far; every other
   platform status is *cross-compiled, not validated*. No platform is
   declared "officially supported" yet.

## Next task (F1 entry)

Implement the Git executor and repository store:
`internal/git/executor` + `internal/storage/repositories` with the
security constraints of docs/security/threat-model.md (argument
allowlist, env allowlist, ID-addressed paths), followed by Smart HTTP
and SSH transports with real E2E tests.

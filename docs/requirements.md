# GitHost requirements specification

Version: 1.0 (F0 baseline) · Derived from the project charter
("PROMPT-MESTRE") and kept in sync with implementation.

## 1. Purpose

GitHost is an independent, self-hosted Git hosting and project management
platform for personal use and small groups. It provides the essential
capabilities of a forge (GitHub/GitLab/Gitea class) — repository hosting,
web UI, issues, pull requests, releases, wiki, REST API — in a single
lightweight binary that runs on a personal server, VPS or NAS without
external services.

## 2. Product constraints

- **C-1** Primary language: Go (stdlib-first).
- **C-2** Architecture: modular monolith (no microservices).
- **C-3** Primary database: SQLite; PostgreSQL is a planned option after
  the core stabilizes.
- **C-4** Git transport: HTTPS and SSH.
- **C-5** Git engine: the **official git executable**; no reimplementation
  of Git object storage; repositories stay standard on disk.
- **C-6** Distribution: single binary + documented external dependencies.
- **C-7** Targets: personal servers; low CPU/RAM footprint.
- **C-8** License: Apache-2.0 (all dependencies must be compatible).
- **C-9** No hard dependency on Docker, Redis, Kubernetes or cloud
  services.
- **C-10** Interface: server-rendered responsive web UI, low JavaScript.
- **C-11** Documentation and status tracking are mandatory deliverables.

## 3. Target platforms

Priority matrix (validated status is tracked per-platform in
[compatibility/platform-matrix.md](compatibility/platform-matrix.md)):
Linux, Windows, macOS, FreeBSD, OpenBSD, NetBSD — each on AMD64 and ARM64.
A platform may only be declared *supported* after native validation;
cross-compilation alone never qualifies as validation.

## 4. Functional requirements

Requirement IDs are referenced by tests, specs and status tracking.

### 4.1 Foundation (F0)

| ID | Requirement |
|---|---|
| FR-001 | Load, validate and default a YAML configuration file; resolve relative paths against the file's directory |
| FR-002 | Structured logging (JSON or text) with selectable level |
| FR-003 | Initialize an instance (`init`): config template, data directory, instance secret, database schema |
| FR-004 | Versioned, transactional, embedded SQL migrations with dirty-database detection |
| FR-005 | Serve HTTP with graceful shutdown on SIGINT/SIGTERM |
| FR-006 | `GET /healthz` reports liveness + database reachability (200/503) |
| FR-007 | `GET /version` reports build metadata |
| FR-008 | CLI: `init`, `serve`, `version`, `doctor` with `--help` and exit codes (0/1/2) |
| FR-009 | `doctor` checks config, data dir writability, secret, git presence/version, database/migrations, port availability |

### 4.2 Git core (F1)

| ID | Requirement |
|---|---|
| FR-101 | Create bare repositories addressed by internal IDs (never user-controlled paths) |
| FR-102 | Git executor wrapping the official git binary: structured args, no shell, context+timeouts, controlled env, resource limits |
| FR-103 | Git Smart HTTP: anonymous read for public repos, authenticated clone/fetch/pull/push (token or password auth) |
| FR-104 | Restricted SSH server: public-key auth, exec-only sessions, only `git-upload-pack`/`git-receive-pack`, no shell |
| FR-105 | User accounts: Argon2id password hashing, session cookies, access tokens with scopes |
| FR-106 | Authorization: per-repository read/write policy enforced on both transports before any git process starts |
| FR-107 | End-to-end proof: create repo → clone (HTTPS and SSH) → commit → push → pull with byte-identical history (`git fsck` clean) |

### 4.3 Web UI and REST API (F2)

| ID | Requirement |
|---|---|
| FR-201 | Dashboard, repository list, repository creation (private by default) |
| FR-202 | File browser, file viewer, commit history, commit detail, diff/compare |
| FR-203 | Branch and tag listing |
| FR-204 | User profile, SSH key and token management pages |
| FR-205 | Repository settings (visibility, default branch, collaborators, archive, rename, delete) |
| FR-206 | Versioned REST API `/api/v1/...`: auth by token with scopes, pagination, structured errors, OpenAPI contract |
| FR-207 | All screens render real backend data; static mockups do not count as done |

### 4.4 Collaboration (F4)

| ID | Requirement |
|---|---|
| FR-401 | Issues: title, Markdown body, open/close, comments, labels, assignees, history |
| FR-402 | Pull requests: source/target branch, commit comparison, diff, comments, reviews (approve/reject), merge, conflict detection |
| FR-403 | Merge strategies explicitly defined; branch protection blocks violating merges |
| FR-404 | Releases from tags with Markdown notes, asset upload/download, integrity checks |
| FR-405 | Wiki (Git-backed), Git LFS (only after full protocol test), MFA |

### 4.5 Operations (F3/F5)

| ID | Requirement |
|---|---|
| FR-301 | Backup: database + repositories + assets + config, with manifests and hashes; consistent snapshot coordination |
| FR-302 | Restore verified against a clean install (restore that does not run is a failure) |
| FR-303 | Packaging: per-OS archives, checksums, Docker Linux image, upgrade/rollback guides |
| FR-501 | Webhooks, repository import/mirror, scheduled jobs, isolated CI runners (F5) |

## 5. Non-functional requirements

| ID | Requirement |
|---|---|
| NFR-001 | Startup time and memory footprint suitable for a 1-core/512 MB VPS |
| NFR-002 | All core code paths covered by automated tests (unit + integration); E2E for transports |
| NFR-003 | No Linux-only syscalls in the core; OS specifics behind interfaces |
| NFR-004 | Concurrent operations on repositories must not corrupt state (SQLite WAL, busy timeouts, per-repo locking) |
| NFR-005 | Server survives abrupt client disconnects, full disks and simultaneous pushes |
| NFR-006 | Structured logs, no secrets in logs |
| NFR-007 | The project must remain `go vet`/`gofmt` clean and fully buildable across the phase matrix at every commit |

## 6. Security requirements (summary)

Full analysis in [security/threat-model.md](security/threat-model.md).

| ID | Requirement |
|---|---|
| SEC-001 | Argon2id for password storage |
| SEC-002 | CSRF, XSS and cookie-hardening protections on the web UI |
| SEC-003 | Rate limiting on authentication endpoints |
| SEC-004 | Tokens with scopes; rotation and revocation for all credentials |
| SEC-005 | Repositories private by default; public registration disabled |
| SEC-006 | Audit log for security-relevant events |
| SEC-007 | Path traversal and Git ref validation on every repository operation |
| SEC-008 | Git subprocesses run with argument allowlists, controlled env, working dir jails and no shell |
| SEC-009 | Arbitrary hooks from repository content are never executed |
| SEC-010 | A critical authorization failure, arbitrary command execution or private repository exposure **blocks release** |

## 7. Out of scope (MVP)

ActivityPub federation, package registries, built-in CI/CD runners beyond
F5's isolated design, GPG commit verification UI, LDAP/OAuth identity
providers. These may be revisited after F6.

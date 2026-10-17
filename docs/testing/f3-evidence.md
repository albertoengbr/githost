# F3 evidence record

Environment: Windows 11 (windows/amd64), Go 1.27.1, Git 2.56.0.windows.1,
Docker 29.8.1 (Linux containers). Executed 2026-10-17.

## 1. The restore gate (F3's defining acceptance test)

`tests/e2e/backup_test.go — TestBackupRestoreGate` runs the full cycle:

1. Live instance A: user with real password, scoped token, private
   repository with a real pushed commit.
2. Server stopped (instance lock released).
3. `backup.Create` → tar.gz with manifest + per-file SHA-256 (SQLite via
   VACUUM INTO snapshot).
4. `backup.Validate` passes; corrupted-manifest archives are rejected
   (unit test `TestCreateValidateRestore`).
5. **Restore into a clean directory** (refuses non-empty targets).
6. Instance B boots from the restored data and, verified with real
   clients:
   - web login works with the pre-backup password (Argon2id hash
     restored) → 303;
   - **the pre-backup token authenticates a git clone over HTTPS**;
   - restored file content is byte-identical;
   - a new commit **pushes** to the restored repository;
   - history contains both pre-backup and post-restore commits;
   - `git fsck --strict` is clean on the restored bare repo;
   - the web UI renders the restored README.
7. Negative: `backup create` while a server holds the lock fails with
   "another GitHost process holds …" (also covered by unit test).

Result: **PASS** — recovery is functional, not just archived.

## 2. Instance lock (internal/lock)

- OS-level (flock / LockFileEx): auto-released on process death — no
  stale locks after crashes.
- `serve` holds it for its lifetime; `backup create` and `backup
  restore` acquire it; doctor reports whether the instance is running.
- Double-serve fails fast. Unit tests: acquire/fail/re-acquire,
  missing-directory error.

## 3. Backup mechanics (unit, internal/backup)

- Manifest-first tar.gz: entry set, sizes and SHA-256 verified at
  create-time (re-hashed while copied) and validate-time.
- Exclusions proven by manifest assertions: `githost.lock`, WAL/SHM side
  files, `.trash-*` remnants; snapshot replaces the live db.
- Tamper test: manifest hash corruption → validation error (flipping
  gzip footer bytes is NOT detected by tar — the test documents that the
  manifest hashes are the integrity authority).
- Restore: staging inside the target volume + rename; refuses non-empty
  data dirs (clean-install requirement).
- List/Prune: deterministic name ordering (timestamp-encoded),
  retention keeps the N newest.

## 4. Native TLS (internal/app tests)

- Self-signed ECDSA P-256 cert generated in-test; `serve` with
  `server.tls.enabled` → `/healthz` answered over HTTPS with a completed
  TLS handshake.
- Unreadable/missing cert/key → Start fails fast with a precise
  `server.tls.*` error.
- Config validation: `tls.enabled` without cert+key → error.

## 5. Push size limits (tests/e2e, real git client)

- Executor `-c` support with strict `key=value` validation (bug found and
  fixed live: keys are case-insensitive camelCase —
  `receive.maxInputSize` — and the first regex rejected capitals).
- With `limits.max_push_bytes: 2500`: small push succeeds; a 16 KiB
  **incompressible** (random) payload is rejected by git itself
  ("pack exceeds maximum allowed size"). Educational finding: the
  first test used repetitive data that deflated below the limit and
  legitimately passed — compressed pack size is what counts.
- Enforcement on both transports: `receive.maxInputSize` via validated
  `-c`, plus `http.MaxBytesReader` on the receive-pack POST.

## 6. Packaging

- `scripts/package.ps1` / `package.sh`: full matrix → per-target
  tar.gz (binary + LICENSE + README) + `SHA256SUMS.txt`.
  Run result: **11 archives + checksums** (netbsd/arm64 skipped,
  unchanged documented limitation).
- **Docker image** (`packaging/docker/Dockerfile`), built and smoke
  tested with real Linux containers:
  - `docker run --rm githost:f3-test version` → linux/amd64 binary;
  - init + doctor inside a volume: all checks OK (git 2.45.4 detected,
    2 migrations applied);
  - serve starts and logs structured JSON;
  - documented: `server.address` must be `0.0.0.0` for published ports.
- Service manager deliverables: hardened systemd unit
  (NoNewPrivileges, ProtectSystem=strict, MemoryDenyWriteExecute,
  SystemCallFilter=@system-service) + install README; launchd plist;
  FreeBSD rc.d script; Windows wrapper README (NSSM/WinSW/Task
  Scheduler — native SCM integration remains a documented gap).
- `tests/packaging_test.go`: CI workflow parses as YAML with the
  required jobs; packaging files exist and the systemd unit keeps its
  hardening directives (drift guard).

## 7. CI

`.github/workflows/ci.yml` defines: test matrix (ubuntu/windows/macos;
race on POSIX runners), cross-compile matrix with artifact upload, and
tag-triggered packaging with checksums. **Not yet executed on a live
runner** — activating the repository on a CI host remains an operator
step (recorded in docs/status.md).

## 8. F3 acceptance criteria — verdict

| # | Criterion (roadmap F3) | Verdict |
|---|---|---|
| Backup + restore with manifest + hashes | PASS (§1–3) |
| Restore verified against a clean install, fully functional | PASS (§1 gate) |
| Security hardening (TLS native, push limits) | PASS (§4–5) |
| Packaging: archives + checksums | PASS (§6, 11/12) |
| Docker Linux image | PASS (§6, built + smoke tested) |
| Service manager integration files | PASS (§6; Windows SCM = documented gap) |
| Multiplatform validation progress | linux/amd64 container-validated; windows/amd64 dev-host validated; rest cross-compiled |
| CI wiring | DEFINED (workflow committed; first live run pending) |

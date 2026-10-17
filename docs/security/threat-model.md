# GitHost threat model — F0 baseline

Method: informal STRIDE over the trust boundaries. This document covers
what exists after F0 and pre-registers the threat analysis for F1 (the
highest-risk phase: it introduces subprocess execution and network
transports that serve untrusted Git clients).

## Assets

1. Repository contents (confidentiality, integrity) — private by default.
2. Metadata database (accounts, permissions, audit trail).
3. Instance secret (`secret.key`) — session/token signing material.
4. Host integrity — GitHost runs local processes; arbitrary command
   execution is the catastrophic failure mode.
5. Availability of the personal server (DoS tolerance).

## Trust boundaries

```text
[Internet / LAN] ── HTTP ──▶ [GitHost process] ──▶ [SQLite + repos on disk]
              ── SSH ──▶     (F1)│
                                └──▶ [git subprocesses]  (F1)
```

- B1: network → process (HTTP today; SSH in F1)
- B2: process → subprocess (git, from F1)
- B3: pushed repository content → process hooks/config (from F1)

## STRIDE — F0 scope (what exists now)

| Threat | Vector (F0) | Mitigation (status) |
|---|---|---|
| Spoofing | Forged requests to /healthz, /version | No auth data exposed; endpoints are read-only facts. Sessions/tokens are F1 with SEC-001..004 |
| Tampering | Malformed config values | Strict validation: ports, levels, drivers, address grammar; absolute-path enforcement |
| Repudiation | Admin cannot prove what happened | `schema_migrations` records applied_at; audit_events table designed for F1 — currently out of scope, tracked |
| Information disclosure | Secret file in logs or repo | Secret written 0600 under data/, .gitignore excludes instance state; logs carry no secret material (verified in F0 tests) |
| DoS | Slow/failed DB, port hijack | Health check timeouts (3s), busy_timeout 5000, doctor reports; rate limiting is F1 SEC-003 (registered) |
| Elevation | Malformed YAML → panic | recover middleware converts panics to 500; fuzz-grade YAML hardening belongs to yaml.v3 (audited dep) |

## STRIDE — F1 pre-registration (must be mitigated before F1 acceptance)

These threats **block the F1 acceptance gate** if unmitigated:

| # | Threat | Requirement |
|---|---|---|
| T1 | **Command injection** via Git URLs/refs → arbitrary command execution | Arguments structured (argv, never a shell), subcommand allowlist, refs validated against a strict pattern before use (SEC-007/008) |
| T2 | **Path traversal** (e.g. `owner/../../etc`) reaching filesystem | Repos addressed by internal UUID; owner/name → ID resolved in SQLite; all derived paths re-validated to be inside the repositories root |
| T3 | **Private repository exposure** via Smart HTTP or SSH | Authorization decision (per-user read/write) happens *before* any git process starts; anonymous access restricted to `visibility=public`; negative tests are part of the E2E gate |
| T4 | **SSH shell escape / arbitrary exec** | Public-key-only auth; reject PTY/shell/direct-tcpip channels; command grammar is exactly `(git-upload-pack|git-receive-pack) <path>`; unknown commands dropped and logged |
| T5 | **Hook smuggling** through pushed content (B3) | Hooks directory is platform-owned; incoming objects are quarantined data, never executable configuration; `receive.fsckObjects` and safe config on receive-pack (SEC-009) |
| T6 | **Credential theft** over cleartext HTTP | Basic auth must ride on TLS (config + docs); token auth with scopes and revocation; rate limit auth endpoints (SEC-002..004) |
| T7 | **Resource exhaustion** via giant pushes/fetches | Size and time limits on receive-pack (pre-receive checks), connection limits, process timeouts |
| T8 | **Environment injection** to git subprocess | Explicit minimal env; allowlisted passthrough of `GIT_PROTOCOL`; no attacker-controlled env keys (protocol research notes this exact requirement) |

## Security requirements mapping

Traceability from docs/requirements.md §6 (SEC-001..010) to mitigations
above is: SEC-007/008 → T1/T2; SEC-005 → T3; SEC-004 → T4/T6;
SEC-009 → T5; SEC-003 → T6/T7. SEC-010 (release-blocking rule) applies
to any of T1–T8 found unresolved at a release gate.

## Residual risk (accepted for F0)

- No TLS termination inside GitHost yet (personal deployments behind a
  reverse proxy are the documented model; native TLS is an F3 task).
- No audit event log yet (table designed, writer arrives with auth in F1).
- Race detector not exercised on the current Windows host (no C
  toolchain); CI must run `scripts/verify.sh` on POSIX (tracked in
  status.md limitations).

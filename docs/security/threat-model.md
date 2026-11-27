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

## STRIDE — F1 pre-registration (mitigation status after F1)

These threats blocked the F1 acceptance gate; each now has an
implemented mitigation and an automated negative test (evidence:
docs/testing/f1-evidence.md §4):

| # | Threat | Mitigation (status) |
|---|---|---|
| T1 | **Command injection** via Git URLs/refs | IMPLEMENTED+TESTED: argv-only execution (never a shell), subcommand allowlist in internal/git, strict SSH command grammar, ref validators |
| T2 | **Path traversal** | IMPLEMENTED+TESTED: UUID-addressed storage with re-validated joins, httpgit.Guard rejects dot segments (400), SSH path grammar resolves owner/name through the store only |
| T3 | **Private repository exposure** | IMPLEMENTED+TESTED: authorization before any git process on both transports; anonymous → 401, stranger → 404, insufficient level → 403; negative e2e on both transports |
| T4 | **SSH shell escape / arbitrary exec** | IMPLEMENTED+TESTED: public-key-only auth, session channels only, shell/PTY/subsystem refused, single allowed command grammar, all tested with raw ssh probes |
| T5 | **Hook smuggling** through pushed content | IMPLEMENTED: GitHost installs no hooks; branch protection is enforced in the application layer (prservice.Merge — single entry point), never through pushed repo content. merge-tree/commit-tree/update-ref are platform-invoked only. |
| T6 | **Credential theft** over cleartext HTTP | **IMPLEMENTED+TESTED**: token-only auth (hash-stored, revocable, scoped); native TLS serving (F3) with fail-fast certificate checks � reverse proxy remains an option |
| T7 | **Resource exhaustion** via giant pushes/fetches | **IMPLEMENTED+TESTED**: hard timeout on every git invocation plus configurable receive.maxInputSize (limits.max_push_bytes, default 512 MiB) enforced by git and the HTTP layer; connection caps remain future work |
| T8 | **Environment injection** to git subprocess | IMPLEMENTED+TESTED: environment rebuilt from an allowlist, GIT_CONFIG_NOSYSTEM/GIT_CONFIG_GLOBAL neutralization (host-config leak unit-tested), GIT_PROTOCOL allowlisted with value validation, `-c` settings strictly validated |

The F3/F5 hardening closed the earlier operational gaps: native TLS,
push-size limits and the race-detector CI job all exist. The remaining
gaps are tracked in docs/status.md and in the post-F6 section below.

## STRIDE — post-F6 security hardening (T9–T12)

| # | Threat | Mitigation (status) |
|---|---|---|
| T9 | **SSRF via webhook URL** (internal port scan / cloud metadata probe through delivery status) | IMPLEMENTED+TESTED: registration-time resolution rejects loopback, unspecified, link-local, multicast, RFC1918, CGNAT and IPv6 ULA targets; dial-time `Control` sees the resolved IP (defeats DNS rebinding); private targets require an explicit `webhooks.allowed_cidrs` entry and loopback stays blocked even if listed; redirects are not followed. Negative tests in `internal/webhooks` and `internal/web`. |
| T10 | **Secret theft from a database leak** (webhook HMAC keys) | IMPLEMENTED+TESTED: webhook secrets are AES-GCM sealed with the instance secret (`ghs1:` prefix), migrated in place on startup, and signing is mandatory — unsigned hooks are never delivered. Recovery codes/tokens stay hash-only, TOTP seeds stay encrypted. Tests in `internal/security`, `internal/store` and `internal/webhooks`. |
| T11 | **TOTP code replay** inside the ±1-step window | IMPLEMENTED+TESTED: the challenge and disable flows atomically claim the current 30-second step (`users.last_totp_step`, migration 0007); a second use of the same step fails, re-enabling MFA resets the mark. Tests in `internal/store` and `internal/web`. |
| T12 | **Credential/token survival after password reset** and **account enumeration / distributed guessing** | IMPLEMENTED+TESTED: all three password-change paths revoke every access token alongside every session; login/MFA rate limits now also key by account; unknown users pay the same Argon2 cost via `auth.VerifyDummy`; SSH authentication attempts are throttled per source IP. Tests in `internal/store`, `internal/cli`, `internal/auth`, `internal/web` and `internal/httpserver`. |

## Security requirements mapping

Traceability from docs/requirements.md §6 (SEC-001..010) to mitigations
above is: SEC-007/008 → T1/T2; SEC-005 → T3; SEC-004 → T4/T6;
SEC-009 → T5; SEC-003 → T6/T7. SEC-010 (release-blocking rule) applies
to any of T1–T8 found unresolved at a release gate.

## Residual risk (current)

- Rate-limit counters are in-process and reset on restart. This is honest
  for a single-binary, single-instance design; a clustered deployment
  would need a shared limiter.
- `server.trusted_proxies` is empty by default: X-Forwarded-* headers are
  ignored until an operator lists their own reverse proxy. Misconfiguring
  that list (trusting an untrusted peer) hands that peer control of the
  client IP used by rate limiting and cookies.
- Webhook targets on private networks are denied unless explicitly
  allowlisted; the allowlist is an operator decision, not a discovery
  mechanism. Loopback/link-local/multicast stay denied unconditionally.
- `server.max_concurrent_requests` defaults to 0 (off): the 503 backpressure
  guard is opt-in because long-lived clones occupy slots. Operators
  exposed to untrusted networks should set it.
- TOTP derivation uses domain-separated SHA-256 rather than HKDF to keep
  seeds encrypted by older versions readable; the instance secret has
  256 bits of entropy, so this remains sound.
- `go test -race` runs on POSIX CI runners but a live runner execution is
  still pending; the Windows dev host has no C toolchain.
- No first-party Windows SCM handler, and netbsd/arm64 remains unsupported
  (dependency limitation) — both are operational, not security, gaps.

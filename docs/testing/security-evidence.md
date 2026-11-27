# Security hardening evidence — 2026-11-27

Companion to [security/threat-model.md](../security/threat-model.md) (T9–T12)
and [status.md](../status.md). This is the source of truth for the
post-F6 security pass. No percentages are used: each control has a named
negative test.

## Verification performed

Host: Windows/amd64, Go 1.27.1, Git available. Commands:

```console
$ gofmt -l .                 # no output
$ go vet ./...               # clean
$ go test ./...              # all packages PASS (including tests/e2e)
```

`go test -race` is not run on this host (no C toolchain); the POSIX CI job
remains the authoritative race run and still needs a live runner execution.

## Control → evidence matrix

| Threat / control | Negative tests (all passing) | Where |
|---|---|---|
| **T9** SSRF via webhook URL | `TestForbiddenWebhookIP` (loopback, `::1`, unspecified, link-local, multicast, RFC1918, CGNAT, IPv6 ULA, IPv4-mapped loopback; allowlist opts in only private ranges; loopback stays blocked), `TestValidateWebhookURL` (public literal IP accepted; private/metadata/userinfo/ftp rejected; allowlist works), `TestWebhookPagesAndCreate` (UI rejects `http://10.0.0.5:8080/hook`) | `internal/webhooks/webhooks_test.go`, `internal/web/ui_pages_test.go` |
| **T9** redirect / rebinding | Client uses `CheckRedirect` → `ErrUseLastResponse`; dial-time `Control` sees the resolved IP on every connection. Covered by the same dial-guard tests; no redirect is followed after registration. | `internal/webhooks/webhooks.go` |
| **T10** secrets at rest | `TestSealRoundTripAndFreshness` (fresh nonce, no plaintext in the value), `TestSealRejectsWrongSecretAndPlaintext`, `TestSealWebhookSecrets` (legacy plaintext sealed once, idempotent, opens with the instance secret), `TestSealedSecretDeliveryAndMandatorySignature` (sealed secret delivers; empty secret yields no delivery) | `internal/security`, `internal/store`, `internal/webhooks` |
| **T10** mandatory signature | UI refuses a short/empty secret (`TestWebhookPagesAndCreate`); delivery refuses a hook with no signing secret before any HTTP request. | `internal/web/handlers_webhooks.go`, `internal/webhooks/webhooks.go` |
| **T11** TOTP replay | `TestClaimTOTPStep` (same step rejected, older step rejected, newer accepted, reset on re-enable), `TestCurrentStepAndVerify`, `TestTOTPCodeCannotBeReplayed` (same valid code cannot clear a second pending challenge) | `internal/store`, `internal/totp`, `internal/web` |
| **T12** token survival after reset | `TestRevokeAllUserTokens` (idempotent, scoped to one user), `TestAdminSetPassword` (CLI reset revokes sessions **and** the token) | `internal/store`, `internal/cli` |
| **T12** enumeration / distributed guessing | `TestVerifyDummy` (dummy hash verifies its own password and is generated once), `TestLoginRateLimit` (existing IP+user budget), `TestLoginAccountRateLimitAcrossAddresses` (sixth guess from a fresh IP still 429), MFA challenge uses an account key too | `internal/auth`, `internal/web` |
| Proxy trust / request caps | `TestProxyHeadersIgnoresUntrustedPeers` (spoofed XFF/XFP ignored), `TestProxyHeadersTrustsConfiguredProxy` (right-most untrusted hop wins; XFP=https marks TLS), `TestConcurrencyLimit` (second request 503 while the slot is held) | `internal/httpserver/proxy_test.go` |
| Retention | `TestPruneAuditAndWebhookDeliveries` (only rows older than the cutoff are removed), `TestRetentionDays` (default 90 days, clamped) | `internal/store`, `internal/jobs` |
| Supply chain | `TestCIWorkflowParses` now requires the `vuln` job; Dependabot config added for gomod + github-actions | `tests/packaging_test.go`, `.github/dependabot.yml` |
| Config validation | `TestValidateRejects` covers malformed `server.trusted_proxies`, negative `server.max_concurrent_requests` and malformed `webhooks.allowed_cidrs` | `internal/config/config_test.go` |

## Operator notes

- `webhooks.allowed_cidrs` is empty by default: private webhook targets are
  denied. Add only the CIDRs you control (e.g. an internal CI runner).
- `server.trusted_proxies` is empty by default: X-Forwarded-* headers are
  ignored. List only your own reverse proxy; otherwise clients can spoof
  their address and rate-limit keys.
- `server.max_concurrent_requests` defaults to 0 (no cap). On an
  untrusted-facing instance set a positive value; long-lived clones hold a
  slot, so size for peak concurrent clients.
- The instance secret (`data/secret.key`) now protects webhook HMAC keys as
  well as TOTP seeds. **Back it up with the database**: losing it makes
  sealed values unreadable (fail-closed) after a restore.
- Password changes now revoke all access tokens. Document any automation
  that relies on a token surviving an operator password reset.
- **Known gap:** the `prune_audit_events` and `prune_webhook_deliveries`
  handlers exist in the job runner, but no production caller enqueues them
  yet — this is also true of the pre-existing `prune_sessions` and
  `rotate_backups` kinds. Until a CLI/scheduler enqueuer is added (follow-up
  item), operators can issue the bounded `DELETE ... WHERE created_at < ?`
  statements directly against the metadata database. Do not claim these
  jobs are automatically scheduled.

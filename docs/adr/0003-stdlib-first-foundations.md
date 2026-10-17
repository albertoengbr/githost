# ADR-0003: stdlib-first foundations

- Status: ACCEPTED
- Date: 2026-10-17

## Context

The charter mandates preferring the Go standard library and forbids
adding dependencies without license/security/need evaluation. F0 needs:
CLI parsing, HTTP serving + routing + middleware, structured logging,
signal handling. All exist in third-party libraries (cobra, chi, zap…)
and in the standard library.

## Decision

F0 is built exclusively on the standard library plus the two audited
dependencies from ADR-0001/0002:

| Need | Choice | Note |
|---|---|---|
| CLI | `flag` per-subcommand | `init/serve/version/doctor`, `--help`, exit codes 0/1/2 |
| HTTP routing | `http.ServeMux` (method patterns, Go 1.22+) | `GET /healthz`, `GET /version`; method mismatch → 405 |
| Middleware | hand-rolled recover + request log (~60 lines) | testable, no framework |
| Logging | `log/slog` JSON/text | one dependency-free logger for app + request logs |
| Lifecycle | `signal.NotifyContext` + `http.Server.Shutdown` | graceful stop on SIGINT/SIGTERM |

## Consequences

- Zero framework lock-in; the exact behavior of every request is
  readable in the repo; upgrades are boring.
- When web UI templating arrives (F2), `html/template` + `go:embed`
  continue the same doctrine. A third-party router (chi) becomes
  justifiable only when middleware composition gets genuinely painful —
  that case must be argued in a new ADR.
- YAML/SQLite exceptions are documented as ADR-0001/0002; any future
  dependency goes through [research/dependency-audit.md](../research/dependency-audit.md).

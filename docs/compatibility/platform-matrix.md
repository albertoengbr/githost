# Platform compatibility matrix

Rules (from the project charter):

- A platform is **SUPPORTED** only after native execution + tests pass on
  that platform. Cross-compilation alone does **not** qualify.
- States: `VALIDATED` (native tests ran and passed), `CROSS_COMPILED`
  (binary builds from another host; not validated), `UNSUPPORTED`
  (cannot build or intentionally excluded).

## Matrix — F0 (2026-10-17)

| OS | amd64 | arm64 | Notes |
|---|---|---|---|
| Windows | **CROSS_COMPILED + native tests** (windows/amd64 host; see below) | CROSS_COMPILED | The development host itself: `go test ./...` passes natively |
| Linux | CROSS_COMPILED | CROSS_COMPILED | No native run yet |
| macOS | CROSS_COMPILED | CROSS_COMPILED | No native run yet |
| FreeBSD | CROSS_COMPILED | CROSS_COMPILED | No native run yet |
| OpenBSD | CROSS_COMPILED | CROSS_COMPILED | No native run yet |
| NetBSD | CROSS_COMPILED | **UNSUPPORTED** | modernc.org/libc v1.77.1 has no netbsd/arm64 port; skip is explicit in scripts |

Honest reading: **no platform is declared officially SUPPORTED yet**.
Windows/amd64 is the strongest case (native build, full test suite,
live smoke test) and becomes VALIDATED once this same suite runs in
CI; other platforms need native runners (F3 task).

## Toolchain requirements

- Go ≥ 1.26 (raised from the initial 1.25 assumption by
  modernc.org/sqlite v1.60.1's own requirement — recorded in ADR-0001).
- Git ≥ 2.30 on the server host (required from F1; doctor warns below
  this version). Verified against Git 2.56.0.windows.1 in F0.

## Validation checklist per platform (F3)

1. `go build ./cmd/githost` on a native toolchain.
2. `scripts/verify.sh` (vet + test + race).
3. Live smoke: `githost init && githost serve` + `/healthz` 200.
4. F1+: real `git clone/push/pull` over both transports.
5. Platform-specific behaviors: file locking, permissions (0600 secret),
   signals (SIGTERM), service manager integration.

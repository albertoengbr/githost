# Testing strategy

## Levels

| Level | Scope | Where | Gate |
|---|---|---|---|
| Unit | package logic in isolation (config validation, SQL splitter, secret handling, health handler semantics) | `*_test.go` beside code | every commit |
| Integration | real SQLite (temp dirs), real HTTP server (`httptest` + real listener), real CLI process flow | package tests `internal/app`, `internal/cli`, `internal/database` | every commit |
| E2E (F1+) | real `git` client against a running GitHost over HTTPS/SSH: clone, push, pull, unauthorized-access attempts | `tests/e2e/` (created in F1) | F1+ acceptance |
| Security (F1+) | negative authorization tests, path-traversal corpus, command-injection corpus | `tests/security/` | F1+ acceptance |
| Compatibility (F3+) | native platform validation runs | `tests/compatibility/` + CI runners | platform declaration |

## Commands (every unit of work)

```console
gofmt -l .          # must print nothing
go vet ./...        # must pass
go test ./...       # must pass
go test -race ./... # must pass on POSIX (see limitation note)
```

`scripts/verify.ps1` (Windows) runs all of the above plus a live binary
smoke test (init + doctor). `scripts/verify.sh` (POSIX) additionally
runs the race detector. CI must run the POSIX variant.

## Non-negotiable rules

1. **No simulated results.** Evidence documents record actual command
   output; limitations are written down, never glossed over.
2. **No mocks as permanent substitutes** for Git, SQLite or HTTP.
3. A feature is ACCEPTED only when its acceptance criteria pass with
   evidence linked from docs/status.md.
4. Real client tests start from F1: genuine `git clone`/`push`/`pull`
   against a real server process, including failure/authorization paths.
5. Tests must be deterministic: no sleeps where a channel/ctx works; no
   fixed ports (bind `:0`).

## Current limitations (tracked, not hidden)

- `-race` cannot run on the current Windows host (no C toolchain; the
  Go toolchain gates `-race` behind cgo there). It is included in the
  POSIX verification path and required in CI.
- Only windows/amd64 has native test execution so far (see
  [compatibility/platform-matrix.md](../compatibility/platform-matrix.md)).

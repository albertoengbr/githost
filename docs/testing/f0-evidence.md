# F0 evidence record

Environment: Windows 11 (windows/amd64), Go 1.27.1, Git 2.56.0.windows.1.
All commands below were executed in the project repository on
**2026-10-17**. Outputs are verbatim (paths abbreviated in places where
temp GUIDs appear).

## 1. Formatting and static checks

```text
$ gofmt -l .
(no output)

$ go vet ./...
(exit 0, no findings)
```

## 2. Unit + integration tests

```text
$ go test ./...
ok  	githost.dev/githost/internal/app	2.073s
ok  	githost.dev/githost/internal/cli	2.008s
ok  	githost.dev/githost/internal/config	0.646s
ok  	githost.dev/githost/internal/database	1.127s
ok  	githost.dev/githost/internal/httpserver	1.322s
ok  	githost.dev/githost/internal/logging	0.704s
ok  	githost.dev/githost/internal/security	0.564s
```

Covered behaviors include: config defaults/validation/relative-path
resolution; embedded config template validity; secret write/load/refuse-
overwrite; SQLite pragma application (journal_mode=wal, busy_timeout=5000)
verified by query, **including a data directory whose path contains spaces**
(real-world Windows layout); migration apply/idempotency/dirty-database
rejection/transactional rollback on failure (verified the failed table and
version record do not survive); healthz 200/503 semantics; version
endpoint; 404/405 routing; panic→500 recovery; request logging levels;
CLI init creating config+data+secret+db and being idempotent; doctor
passing after init and failing with hint before init; serve refusing a
missing config with hint; app Start/Addr/Stop lifecycle against a real
listener (ephemeral port); Start failing on an occupied port.

Race detector: **not run on this host** — `go test -race` requires cgo on
Windows and no C toolchain exists here (verified:
`go test -race` → "go: -race requires cgo; enable cgo by setting
CGO_ENABLED=1", and with CGO_ENABLED=1 → `cgo: C compiler "gcc" not
found`). Required in POSIX CI via scripts/verify.sh. Tracked in
docs/status.md limitations.

## 3. Verification script (build + smoke test)

```text
$ powershell -ExecutionPolicy Bypass -File scripts\verify.ps1
==> gofmt
==> go vet
==> go test               (all packages ok, as above)
==> go build
==> smoke test (init/doctor/healthz)
created configuration file ...\githost-verify-<guid>\githost.yaml
GitHost instance initialized
  data directory: ...\githost-verify-<guid>\data
  database:       ...\githost-verify-<guid>\data\githost.db
  secret file:    ...\githost-verify-<guid>\data\secret.key
Run "githost serve" to start the server.
[ok  ] configuration      ...\githost.yaml
[ok  ] data directory     ...\data
[ok  ] instance secret    ...\data\secret.key
[ok  ] git                2.56.0.windows.1
[ok  ] database           1 migrations applied
[ok  ] http port          127.0.0.1:3000 available
All checks passed.
```

## 4. Live server exercise (real process, real HTTP)

```text
$ .\bin\githost.exe version
githost version 0.1.0-dev
  commit:     none
  build date: unknown
  platform:   windows/amd64

$ .\bin\githost.exe serve --config ...\githost.yaml
githost 0.1.0-dev listening on http://127.0.0.1:3000

$ curl http://127.0.0.1:3000/healthz
{"database":"ok","status":"ok","version":"0.1.0-dev"}

$ curl http://127.0.0.1:3000/version
{"arch":"amd64","build_date":"unknown","commit":"none","go":"go1.27.1","os":"windows","version":"0.1.0-dev"}
```

Structured JSON logs emitted by the server process (stdout):

```json
{"time":"2026-10-17T14:17:10.6645614-03:00","level":"INFO","msg":"http_server_started","addr":"127.0.0.1:3000"}
{"time":"2026-10-17T14:17:12.6902784-03:00","level":"INFO","msg":"http_request","method":"GET","path":"/version","status":200,"duration_ms":0,"remote":"127.0.0.1:63734"}
```

Note: `/healthz` probes log at DEBUG (level=info in the smoke config), so
they do not spam production logs — the captured stdout above shows the
`/version` request but not `/healthz`, by design.

## 5. Cross-compilation matrix

```text
$ powershell -ExecutionPolicy Bypass -File scripts\cross-compile.ps1
==> building linux/amd64      ok
==> building linux/arm64      ok
==> building windows/amd64    ok
==> building windows/arm64   ok
==> building darwin/amd64    ok
==> building darwin/arm64     ok
==> building freebsd/amd64    ok
==> building freebsd/arm64    ok
==> building openbsd/amd64    ok
==> building openbsd/arm64    ok
==> building netbsd/amd64     ok
==> SKIPPED netbsd/arm64 (UNSUPPORTED: modernc.org/libc has no netbsd/arm64 port)
Cross build complete.      (11 binaries, ~15.7–17.3 MB each)
```

The netbsd/arm64 attempt failed with
`imports modernc.org/libc/...: build constraints exclude all Go files`
before being converted into an explicit, documented skip. This is a
**dependency limitation, not a validated platform claim** — see
[compatibility/platform-matrix.md](../compatibility/platform-matrix.md).

## 6. F0 acceptance criteria — verdict

| # | Criterion (roadmap) | Verdict |
|---|---|---|
| 1 | Application starts; configuration loads and validates | PASS (§3, §4) |
| 2 | Database initialized and migrated (idempotent, dirty-safe) | PASS (§2, §3) |
| 3 | vet/gofmt/tests green on the native host | PASS (§1, §2; race: deferred to POSIX CI — documented) |
| 4 | Cross-compile matrix attempted, results recorded | PASS (§5; 11 build, 1 unsupported) |
| 5 | Documentation consistent with implementation | PASS (docs/ set matches shipped code) |

**F0 is ACCEPTED.** Next: F1 (Git core) with the E2E clone/push/pull
gate defined in the roadmap.

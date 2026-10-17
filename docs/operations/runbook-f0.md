# Runbook — F0 operations

How to install, run and verify a GitHost instance with the current (F0)
feature set. Update this file with every phase.

## Requirements

- Windows/Linux/macOS/BSD on amd64 (arm64 builds exist; see
  compatibility matrix)
- Go ≥ 1.26 (only when building from source)
- Git ≥ 2.30 (not used by F0 features; required from F1)

## Install from source

```console
$ git clone <your-githost-repo> githost
$ cd githost
$ go build -trimpath -o githost ./cmd/githost
```

Or use the scripts: `scripts\verify.ps1` (Windows) / `scripts/verify.sh`
(POSIX) for a full check, `scripts\cross-compile*` for the OS/arch
matrix into `dist/`.

## First run

```console
$ ./githost init          # creates githost.yaml + data/ (config template,
                          # data dir, secret.key, githost.db, migrations)
$ $EDITOR githost.yaml   # review address/port; see comments in the file
$ ./githost serve
githost 0.1.0-dev listening on http://127.0.0.1:3000
```

`serve` applies pending migrations on every start (idempotent) and shuts
down cleanly on Ctrl+C / SIGTERM (up to 10s drain).

## Health and diagnostics

```console
$ curl -s http://127.0.0.1:3000/healthz   # {"status":"ok","database":"ok",...}
$ ./githost doctor                       # config, data dir, secret, git,
                                         # database, port checks
$ ./githost version
```

Doctor exit codes: 0 all critical checks pass; 1 something failed
(warnings like a missing Git or an occupied port do not fail the run).

## Data layout

```text
githost.yaml        config (edit freely; init never overwrites it)
data/
  githost.db        SQLite metadata (+ -wal/-shm side files, normal)
  secret.key        instance secret — keep private, never commit
```

Back up `githost.yaml` + the whole `data/` directory after stopping the
server (proper managed backup/restore arrives in F3).

## Troubleshooting

| Symptom | Diagnosis |
|---|---|
| `hint: run "githost init" first` | No config file at the `--config` path |
| `listen on ...: bind: ...` | Port occupied — `doctor` reports it; change `server.http_port` |
| healthz 503 | Database unreachable: check disk, file permissions, data path |
| `database is dirty: applied version NNNN is not present` | Binary older than the schema — run a matching or newer build; never downgrade |

## Service manager integration (F0 guidance)

Run `githost serve` under the OS service manager so it starts at boot
and restarts on failure. Structured JSON logs go to stdout — capture
them in the journal/syslog wrapper. Concrete unit files (systemd,
launchd, Windows Service, rc.d) are F3 packaging deliverables.

# GitHost

GitHost is a lightweight, self-hosted Git platform for personal and
small-team use: a single Go binary that hosts Git repositories, serves them
over HTTPS and SSH, and provides a web interface, REST API, issues, pull
requests and releases — without depending on any external service.

> **Status:** F0–F6 complete — see [docs/status.md](docs/status.md) for the
> authoritative feature state. GitHost ships Git transports (clone/push/pull
> over HTTPS and SSH), a web UI, a REST API, issues, pull requests, releases,
> labels, webhooks, search and admin user management.

## Design goals

- **Independent**: no GitHub/GitLab/cloud dependency; SQLite storage; no
  Redis/Docker/Kubernetes requirement.
- **Official Git engine**: repositories are plain bare Git repos operated
  via the official `git` executable — history stays 100% standard.
- **Multiplatform**: Linux, Windows, macOS, FreeBSD, OpenBSD and NetBSD
  (amd64/arm64).
- **Private and secure by default**: repositories are private, public
  registration is disabled, secrets are generated, not shared.
- **Low footprint**: single binary, embedded assets, modest CPU/RAM.

## Requirements

- Go **1.26 or newer** (to build; the SQLite driver sets this floor — see ADR-0001)
- Git **2.30 or newer** (on the server, from F1 onward)

## Quickstart

```console
$ go build -o githost ./cmd/githost
$ ./githost init          # creates githost.yaml, data/, secret, database
$ ./githost serve         # http://127.0.0.1:3000
```

Useful checks:

```console
$ curl -s http://127.0.0.1:3000/healthz
{"status":"ok","database":"ok","version":"0.1.0-dev"}
$ ./githost doctor
```

## Commands

| Command | Purpose |
|---|---|
| `githost init` | Create/repair an instance (config, data dir, secret, schema) |
| `githost serve` | Run the HTTP server (Ctrl+C stops it cleanly) |
| `githost doctor` | Verify config, data dir, secret, git, database, port |
| `githost version` | Print build metadata |

## Development

```console
$ make verify            # fmt + vet + test + race
$ make cross             # build the full OS/arch matrix into dist/
```

On Windows use `scripts\verify.ps1` and `scripts\cross-compile.ps1`.

## Documentation

- [docs/status.md](docs/status.md) — **source of truth** for progress
- [docs/requirements.md](docs/requirements.md) — requirements specification
- [docs/roadmap.md](docs/roadmap.md) — phases F0–F6 and acceptance criteria
- [docs/architecture/](docs/architecture/) — system design
- [docs/security/](docs/security/) — threat model and hardening
- [docs/testing/](docs/testing/) — test strategy and evidence
- [docs/adr/](docs/adr/) — architecture decision records

## References

GitHost is an independent project. Gogs, Gitea and Forgejo are studied as
technical references only (see docs/research/); no code is copied from them.

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright © 2026 Eng. Alberto José Oliveira Pereira
<alberto@alberto.eng.br>

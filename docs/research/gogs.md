# Research: Gogs

> Reference project study for GitHost. No code is copied from Gogs; this
> document records architectural findings only. Gogs is MIT licensed, which
> permits reference and (if ever needed, with attribution) selective reuse.

- Project: <https://github.com/gogs/gogs> (observed 2026-10-17)
- License: **MIT**
- Language: Go. Single-binary distribution is a core design goal.

## What Gogs is

"The painless way to host your own Git service" — a self-hosted Git server in a
single Go binary targeting Linux, macOS, Windows and ARM. It explicitly aims at
low-end hardware (a Raspberry Pi or a small VPS; the project cites ~64 MB RAM
Docker deployments and 512 MB RAM as a team baseline).

## Findings relevant to GitHost

| Area | Gogs approach | GitHost takeaway |
|---|---|---|
| Distribution | One static binary per platform, config + templates alongside | Confirms single-binary feasibility; we additionally embed assets via `go:embed` |
| Process model | One process serving HTTP + SSH | Adopt: one process, both transports (SSH arrives in F1) |
| Database | SQLite3 / MySQL / PostgreSQL / others | We standardize on SQLite first (see ADR-0001); PostgreSQL is a later option |
| Git transport | `https://` and `ssh://` clone/push, plus HTTP | Confirms scope of F1 |
| Config | INI file (`conf/`) | We use YAML instead (ADR-0002); INI parsing quirks are a known pain point |
| Web UI | Server-rendered templates + static assets, minimal build step | Supports our "low JavaScript" requirement |
| CLI | `cmd/gogs` with subcommands (web, serv, hook, admin, ...) | Confirms subcommand-style CLI, implemented with stdlib `flag` (ADR-0003) |
| Repo layout | `cmd/` for entrypoints, `internal/` for private packages, `conf/`, `templates/`, `public/` | Similar split adopted for GitHost |

## Notable features (scope reference for later phases)

Issues, pull requests, wiki, protected branches, webhooks (Slack/Discord/Dingtalk),
repository Git hooks, deploy keys, Git LFS, mirror/migrate, web editor, 2FA,
localization (31 languages), experimental API.

## Conclusions for GitHost

1. The "painless single binary + SQLite" posture is proven at scale by Gogs —
   it matches GitHost's personal-server target and the low CPU/RAM requirement.
2. Gogs proves that SSH + Smart HTTP can live inside one Go process without a
   separate frontend proxy.
3. Gogs' main architectural lesson (and the reason Gitea forked it) is that a
   monolith needs disciplined module boundaries; GitHost formalizes this with
   the layer rules in `docs/architecture/overview.md`.

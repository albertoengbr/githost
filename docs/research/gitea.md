# Research: Gitea

> Reference project study for GitHost. No code is copied from Gitea; this
> document records architectural findings only. Gitea is MIT licensed.

- Project: <https://github.com/go-gitea/gitea> (observed 2026-10-17)
- License: **MIT**
- Language: Go; "works across all the platforms and architectures that are
  supported by Go, including Linux, macOS, FreeBSD/OpenBSD and Windows on x86,
  amd64, ARM, RISC-V 64 and PowerPC".

## What Gitea is

A self-hosted "all-in-one software development service" (forked from Gogs in
2016): Git hosting, code review, issues, kanban, wiki, package registry and
CI/CD compatible with GitHub Actions. Demo at demo.gitea.com.

## Findings relevant to GitHost

| Area | Gitea approach | GitHost takeaway |
|---|---|---|
| Module layout | `cmd/` (entrypoints), `models/` (domain+DB), `modules/` (shared infrastructure: git, setting, log...), `routers/` (HTTP), `services/` (use cases), `templates/`, `public/`, `options/` | The `models/modules/routers/services` split validates our layered monolith (domain / infra / transport / application services) |
| Git integration | Wraps the official `git` executable via a `modules/git` package (command execution, hooks, diffs); does not reimplement Git | Confirms the "official Git executable" requirement (spec §7) is the proven approach |
| Config | `app.ini` with an annotated `app.example.ini`; split between static and dynamic (admin-panel editable) options | We keep a single static YAML file in F0; dynamic settings are deferred |
| Platforms | FreeBSD/OpenBSD are first-class release targets | Matches our 6-OS support matrix (docs/compatibility/platform-matrix.md) |
| DB support | SQLite, MySQL/MariaDB, PostgreSQL out of the box | Confirms SQLite as a credible primary backend |
| CLI | `gitea web`, `gitea help`, plus `gitea doctor`, `gitea migrate`, `gitea admin` | Confirms our `init/serve/version/doctor` F0 CLI and later `migrate/admin` subcommands |
| Release process | Cross-compiled binaries per OS/arch published with checksums | Adopted in `scripts/cross-compile.ps1` and Makefile `cross` target |
| Web UI | Heavy SPA-ish frontend (webpack/vite, Vue components) | We deliberately diverge: server-rendered `html/template` with minimal JS (spec §12) |

## Conclusions for GitHost

1. Gitea demonstrates the practical shape of a Git-hosting monolith in Go; its
   biggest architectural risk is module sprawl (21k+ commits, very large
   `models/` layer) — GitHost mitigates by starting minimal and adding layers
   only when a phase requires them.
2. Wrapping the official `git` binary is validated at the largest scale in this
   domain; GitHost's F1 executor follows the same principle with a strict
   argument allowlist (spec §7.3).
3. Gitea's breadth (package registries, Actions, etc.) is *out of scope* for
   GitHost's MVP; our feature set follows the F0–F6 roadmap exactly.

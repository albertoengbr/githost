# Dependency audit — F0

Rule (charter §25.5): no dependency is added without evaluating license,
security and need. Project license: **Apache-2.0**, so every dependency
must be permissive-compatible (MIT/BSD/Apache-2.0/public domain).
**GPL/LGPL/AGPL code is forbidden.**

## Direct dependencies

| Module | Version | License (verified from module cache) | Need | Verdict |
|---|---|---|---|---|
| `gopkg.in/yaml.v3` | v3.0.1 | Dual **MIT / Apache-2.0** (LICENSE file in module) | Config file format (ADR-0002) | APPROVED |
| `modernc.org/sqlite` | v1.60.1 | **BSD-3-Clause**; embeds SQLite (public domain) | Pure-Go SQLite, no CGO (ADR-0001) | APPROVED |

Note: `modernc.org/sqlite` v1.60.1 requires **go 1.26**, which sets the
project's minimum Go version (go.mod: `go 1.26.0`).

## Indirect dependencies (module graph)

| Module | License | Role |
|---|---|---|
| `modernc.org/libc` v1.77.1 | BSD-3-Clause | libc emulation for sqlite |
| `modernc.org/mathutil`, `memory`, `fileutil`, `strutil`, `sortutil` | BSD-3-Clause | sqlite support |
| `golang.org/x/sys` v0.48.0 | BSD-3-Clause | syscall shims |
| `github.com/dustin/go-humanize` v1.0.1 | MIT | size formatting |
| `github.com/google/uuid` v1.6.0 | BSD-3-Clause | UUIDs |
| `github.com/mattn/go-isatty` v0.0.24 | MIT | TTY detection |
| `github.com/ncruces/go-strftime` v1.0.0 | MIT | SQLite time formatting |
| `github.com/remyoudompheng/bigfft` | MIT (BSD-style) | FFT math for sqlite |

All permissive; all reachable only through the two approved direct
dependencies. No GPL-family code anywhere in the graph (`go list -m all`
reviewed at F0).

## Planned future dependencies (pre-registered)

| Module | License | Phase | Purpose |
|---|---|---|---|
| `golang.org/x/crypto` | BSD-3-Clause | F1 | SSH server (`ssh`), Argon2id (`argon2`) |

## Reference-project code policy

- Gogs, Gitea: MIT — may be *referenced* freely; any future adaptation
  of a snippet must carry attribution. Nothing copied so far.
- Forgejo: **GPL-3.0+ since v9.0** — code must never be copied into
  GitHost (see docs/research/forgejo.md for the compliance rule).

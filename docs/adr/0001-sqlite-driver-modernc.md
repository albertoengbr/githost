# ADR-0001: SQLite driver — pure Go (modernc.org/sqlite), no CGO

- Status: ACCEPTED
- Date: 2026-10-17

## Context

GitHost must run on Linux, Windows, macOS, FreeBSD, OpenBSD and NetBSD
(amd64/arm64) and ship as a single self-contained binary per target.
SQLite is the mandatory primary database. Two realistic Go drivers
exist:

1. `github.com/mattn/go-sqlite3` — mature, fast, **requires CGO**: every
   target needs a working C cross-toolchain or a native build.
2. `modernc.org/sqlite` — SQLite transpiled to Go, slower, **no CGO**:
   cross-compiles with the plain Go toolchain everywhere Go does.

## Decision

Use `modernc.org/sqlite` (v1.60.1 at the time of writing). Configure
connections via DSN pragmas (WAL, busy_timeout=5000, synchronous=NORMAL,
foreign_keys=ON) and keep a small connection pool (4).

## Consequences

- **Positive**: the entire support matrix builds from any host with just
  the Go toolchain (`scripts/cross-compile.ps1` proves 11/12 targets);
  no cgo runtime quirks per platform; static binaries.
- **Negative**: measured throughput is below the C driver. Acceptable at
  GitHost's scale (personal/small team); WAL + prepared statements keep
  it far from being the bottleneck for a forge.
- **Found limitation**: `modernc.org/libc` (v1.77.1) has no
  **netbsd/arm64** port, so that single matrix target fails to build.
  Recorded honestly in
  [compatibility/platform-matrix.md](../compatibility/platform-matrix.md);
  scripts skip it explicitly. Options if NetBSD/arm64 becomes a real
  requirement: wait for upstream support, or produce a CGO build with a
  native toolchain (documented escape hatch).
- **Toolchain note**: the driver's module requires `go 1.26`, which
  raises the project's minimum Go version accordingly (go.mod reflects
  `go 1.26.0`).

## Alternatives considered

- `mattn/go-sqlite3` — rejected: makes the 6-OS matrix a cgo
  cross-compilation project, reintroducing per-platform C toolchains the
  project explicitly avoids.
- Pure-Go rewrites of Git object storage — not applicable here; also
  forbidden by the charter (official git executable only).

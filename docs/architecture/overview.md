# GitHost architecture overview

## Shape: a modular monolith

GitHost is a single process, a single binary, a single SQLite database
file and one directory tree of bare Git repositories. The complexity of
the system is intentionally bounded: this is personal/small-team
software, and every architectural element must justify itself.

Layers, from the outside in:

```text
            ┌────────────────────────────────────────────┐
 transports │  HTTP (web UI, API, Git Smart HTTP)        │
            │  SSH (Git-only, F1)                        │
            ├────────────────────────────────────────────┤
            │  authn/authz middleware (F1)               │
            ├────────────────────────────────────────────┤
 application│  application services (CLI, wiring)        │
            ├────────────────────────────────────────────┤
            │  domain logic (users, repos, issues, PRs)  │
            ├────────────────────────────────────────────┤
            │  adapters: SQLite · git executor · storage │
            ├────────────────────────────────────────────┤
 platform  │  Go stdlib · official git binary            │
            └────────────────────────────────────────────┘
```

Key decisions (details in [adr/](../adr/)):

| # | Decision | ADR |
|---|---|---|
| 1 | Project named GitHost, module `githost.dev/githost` | [0000](../adr/0000-project-naming.md) |
| 2 | SQLite via pure-Go `modernc.org/sqlite` (no CGO) | [0001](../adr/0001-sqlite-driver-modernc.md) |
| 3 | YAML configuration (`gopkg.in/yaml.v3`) | [0002](../adr/0002-configuration-format-yaml.md) |
| 4 | stdlib-first: `flag` CLI, `net/http`, `log/slog` | [0003](../adr/0003-stdlib-first-foundations.md) |
| 5 | Embedded versioned SQL migrations + custom runner | [0004](../adr/0004-migrations-embedded-sql.md) |

## Principles

1. **Dependency direction**: transports depend on services; services
   depend on domain; domain depends on nothing internal. Adapters are
   reached through small interfaces defined where they are *used*.
2. **Errors are values**: every layer returns wrapped errors with
   context; panics only cross the HTTP middleware boundary (logged,
   answered 500).
3. **Context propagation**: every operation that can block takes a
   `context.Context`; subprocesses are cancellable.
4. **Explicit wiring** (`internal/app`): no service locators, no
   reflection magic; construction happens in one place.
5. **Stdlib first**: a third-party dependency must earn its place
   (license-audited, see [research/dependency-audit.md](../research/dependency-audit.md)).
6. **Testable modules**: packages communicate through their public API;
   tests use real SQLite and real processes wherever practical; mocks are
   for external boundaries only and never permanent (project rule).

## Request flow (F0)

```text
githost serve
  └─ config.Load → logging.New → database.Open → database.Migrate
  └─ httpserver.New(DB) → middleware (recover → requestLog) → mux
       GET /healthz → PingContext(db, 3s) → 200/503 JSON
       GET /version → buildinfo JSON
  └─ http.Server.Serve(listener)
  └─ SIGINT/SIGTERM → context cancel → http.Shutdown → db.Close
```

## Git engine boundary (F1 design boundary)

The official `git` executable is the **only** engine that touches
repository contents. GitHost talks to it through a single adapter with:

- **Argument allowlists** per command; never concatenated strings;
  never a shell.
- **Controlled environment**: minimal env (`PATH`, `HOME`-equivalent
  repo dir, allowlisted `GIT_PROTOCOL`), absolute working directory,
  timeouts, output capture limits.
- **Path discipline**: on-disk repositories are addressed by internal
  IDs; owner/name resolution happens in SQLite *before* any path is
  constructed; no user input reaches a filesystem path unvalidated.
- **Hook policy**: server-side hooks are installed by GitHost only;
  hooks shipped inside pushed content are never executed.

Rationale and protocol details:
[research/git-protocols.md](../research/git-protocols.md).

## Data layout on disk (F0)

```text
<config-dir>/githost.yaml        configuration (created by init)
<data>/githost.db                SQLite metadata (+ -wal/-shm in WAL mode)
<data>/secret.key                instance secret (0600)
<data>/repositories/<id>.git/    bare repositories (F1)
<data>/backups/                  backup artifacts (F3)
```

Physical repositories and SQL metadata are strictly separated: the
database never stores Git objects, and Git never touches the database.

## Process model

One process serves HTTP now and SSH (F1) from the same binary. Service
managers own the lifecycle (systemd/launchd/Windows Service/rc.d);
GitHost itself just handles SIGINT/SIGTERM gracefully and logs to
stdout in JSON.

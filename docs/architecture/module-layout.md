# Module layout

```text
githost/
├── cmd/githost/            process entrypoint (thin: CLI dispatch only)
├── internal/
│   ├── app/                composition root: Init, Start, Stop; owns lifecycle
│   ├── buildinfo/           version metadata injected via -ldflags
│   ├── cli/                 subcommands: init, serve, version, doctor
│   ├── config/              YAML load, defaults, path resolution, validation
│   │                        (embedded config.example.yaml lives here)
│   ├── database/            SQLite open (pragmas), statement splitter,
│   │                        migration runner (transactional, dirty-safe)
│   ├── httpserver/          routes, healthz/version handlers, middleware
│   │   └── (F1: httpgit/    Git Smart HTTP service endpoints)
│   ├── logging/             slog construction (JSON/text, level)
│   ├── security/            instance secret generation/loading
│   │   └── (F1+: auth/,     argon2id, sessions, tokens, csrf)
│   │   └── (F1: sshgit/     restricted SSH transport)
│   │   └── (F1: git/         executor: the ONLY caller of the git binary)
│   │   └── (F1: storage/     ID-addressed bare repository store)
│   │   └── (F1: permissions/ per-repository read/write policy)
│   │   └── (F1: audit/       security event log)
│   │   └── (F1+: api/, web/, search/, backup/, jobs/, events/)
│   └── domain/              (F1+) user/, repository/, issue/, pullrequest/,
│                            release/ — pure business rules, no I/O
├── migrations/              NNNN_name.sql, embedded via go:embed
├── web/                     (F2+) templates/, static/ embedded assets
├── tests/                   (F1+) integration/, e2e/, security/, compatibility/
├── packaging/               (F3+) per-OS service definitions, Dockerfile
├── scripts/                 verify + cross-compile automation
└── docs/                    this documentation set
```

Rules:

1. `cmd/` stays trivial — all behavior lives in `internal/` packages
   that are importable by tests.
2. Packages under `internal/` may not import each other sideways;
   dependencies follow the layer arrows in
   [overview.md](overview.md).
3. `internal/domain` (from F1) never imports adapters; it declares the
   interfaces adapters implement.
4. Every new package lands with a `_test.go` file in the same PR-sized
   unit of work that introduces it.
5. The `web/` tree is embedded (`go:embed`) so the binary stays
   self-contained (spec §12).

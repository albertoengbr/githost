# ADR-0000: Project naming and module path

- Status: ACCEPTED
- Date: 2026-10-17
- Deciders: project owner

## Context

The project charter named the platform provisionally "GitPessoal".
Before the first commit, the repository directory was created as
`githost` and the owner selected a definitive name. The Go module path
must be fixed early: changing it later touches every import in the
codebase.

## Decision

- Product name: **GitHost**.
- Go module path: **`githost.dev/githost`**.
- Binary: `githost`; CLI invoked as `githost <command>`.

## Consequences

- All documentation and CLI strings say GitHost; the charter's
  "GitPessoal" references are superseded (the charter is honored in
  substance — the requirements, phases and rules are unchanged).
- `githost.dev` is not owned by the project; this is acceptable for an
  application (as opposed to an importable library): nothing ever
  `go get`s this module from a public proxy. If the project is ever
  published, the path can be re-pointed or a redirect host stood up
  before external consumers exist.

## Alternatives considered

- `github.com/<user>/githost` — couples the identity to a hosting
  platform, which conflicts with the project's independence principle.
- Keeping "GitPessoal" — superseded by owner decision.

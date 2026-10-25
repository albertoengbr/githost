# GitHost documentation

This directory is the project's documentation set. **docs/status.md is the
source of truth for progress**; never rely on commit messages or memory to
determine what works.

| Document | Purpose |
|---|---|
| [getting-started.md](getting-started.md) | First-access tutorial: install from a release, create the admin, log in, push the first commit |
| [requirements.md](requirements.md) | Requirements specification (functional, non-functional, security, constraints) |
| [roadmap.md](roadmap.md) | Phases F0–F6, acceptance criteria, current state |
| [status.md](status.md) | Per-feature state: NOT_STARTED / SPECIFIED / IMPLEMENTING / TESTING / BLOCKED / ACCEPTED, with evidence |
| [architecture/](architecture/) | System overview, module layout, database schema plan |
| [specifications/](specifications/) | Phase specifications with verifiable acceptance criteria |
| [research/](research/) | Reference project studies (Gogs, Gitea, Forgejo, Git protocols) and dependency audits |
| [security/](security/) | Threat model and security requirements |
| [testing/](testing/) | Test strategy and recorded evidence |
| [compatibility/](compatibility/) | Platform support matrix (validated vs cross-compiled only) |
| [operations/](operations/) | Runbooks: install, run, upgrade, backup/restore |
| [adr/](adr/) | Architecture Decision Records |

## Conventions

- Documentation is written in English. Code identifiers, log messages and
  API payloads are also English.
- Feature states follow the project method: a feature is only ACCEPTED when
  its acceptance criteria pass with recorded evidence. No subjective
  completion percentages are used, ever.
- Every architectural decision of consequence gets an ADR before
  implementation, not after.

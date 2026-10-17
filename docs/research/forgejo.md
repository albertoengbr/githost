# Research: Forgejo

> Reference project study for GitHost. **No code is copied from Forgejo.**
> Forgejo is distributed under the terms of **GPL-3.0-or-later since v9.0**
> (versions before v9.0 were MIT, verified 2026-10-17 on the repository's
> LICENSE and README). GPL is incompatible with GitHost's Apache-2.0 license;
> Forgejo is therefore studied for process and architecture ideas only —
> never as a code source.

- Project: <https://codeberg.org/forgejo/forgejo> (observed 2026-10-17)
- License: **GPL-3.0-or-later** (since v9.0; pre-9.0 was MIT)
- Language: Go (fork of Gitea, created 2022 for community independence)

## What Forgejo is

A community-governed Gitea fork emphasizing independence, privacy-first
defaults and Federation (ActivityPub, WIP). Hosted on Codeberg, running its own
CI ("Forgejo Actions").

## Findings relevant to GitHost

| Area | Forgejo approach | GitHost takeaway |
|---|---|---|
| Governance | Nonprofit-backed, community-owned | GitHost is personal/small-group software; governance is out of scope, but the "independent by design" posture aligns with the spec's no-external-services rule |
| Privacy defaults | Update checker opt-in, conservative defaults | GitHost adopts "private by default" and "registration disabled by default" in F0 config |
| Testing culture | CI matrix observed live on the repo: unit tests, SQLite integration, MySQL/PostgreSQL integration, MariaDB matrix, E2E (playwright), `semgrep/ci` security scan, release-process simulation | GitHost mirrors the *shape*: unit → integration → e2e, plus a security pass; see docs/testing/strategy.md |
| Hardening posture | Recent commits observed include security patches (e.g. 2026-10-12 security patch set), `receive.hideRefs` git config fixes | Hiding refs / controlling receive-pack behavior is noted for GitHost F1 (prevents probing of hidden refs on push) |
| Release discipline | Release-notes assistant, published security advisories | GitHost records evidence + status per phase in docs/status.md |

## Compliance rule (binding)

- Gogs, Gitea: MIT — compatible with Apache-2.0; reference freely, never copy
  blindly, attribute if any snippet is ever adapted.
- Forgejo: **GPL-3.0+** — code must never be copied into GitHost. Reading its
  public documentation and commit history for learning is fine; transcribing
  its code is a license violation and is prohibited by this project's rules.

## Conclusions for GitHost

1. Forgejo's engineering discipline (wide test matrix, security-first CI) is
   the most valuable thing to imitate — process, not code.
2. Its existence validates the demand for independent, self-hostable forges
   with conservative defaults — exactly GitHost's niche, at a smaller scale.
3. The GPL/MIT license split in this space is a live compliance hazard; every
   future dependency must pass the audit in docs/research/dependency-audit.md.

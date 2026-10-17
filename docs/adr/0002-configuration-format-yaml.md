# ADR-0002: Configuration format — YAML

- Status: ACCEPTED
- Date: 2026-10-17

## Context

GitHost needs one human-editable configuration file. Constraints:
license-compatible dependency (Apache-2.0 project), small dependency
count, readable defaults with comments, forward-compatible structure as
features arrive per phase.

## Decision

YAML 1.2 via `gopkg.in/yaml.v3` (v3.0.1; dual MIT/Apache-2.0 license —
verified in [research/dependency-audit.md](../research/dependency-audit.md)).
One file, `githost.yaml`, with these loading rules:

- Defaults are applied first; the file overlays them (unknown fields are
  tolerated for forward compatibility).
- Relative paths resolve against the **directory containing the config
  file**, so an instance directory is self-contained and movable.
- After loading, all paths are absolute and the configuration is
  **validated** with precise field-level errors (`server.http_port: 0
  is out of range 1..65535`).
- `githost init` writes the embedded, commented template only when the
  file does not exist; user edits are never clobbered.

## Consequences

- One well-known dependency; comments and hierarchies map well to the
  phase-gated config surface (SSH section already reserved for F1).
- The YAML ecosystem's foot-guns ( Norway problem, implicit typing) are
  mitigated by strict post-parse validation of every field.
- Alternative TOML was rejected as an equal-cost choice with no
  decisive advantage here; INI (Gogs/Gitea style) was rejected for
  weak structure and a homemade parser would add unowned code.

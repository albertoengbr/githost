# Research: Git transport protocols (Smart HTTP, protocol v2, SSH)

> Distilled from the official Git documentation (observed 2026-10-17):
> <https://git-scm.com/docs/http-protocol>, <https://git-scm.com/docs/protocol-v2>.
> These notes define what GitHost's F1 transports must implement.

## 1. Smart HTTP protocol

Git over HTTP has a "dumb" and a "smart" mode. GitHost implements **smart
only** (the dumb protocol is legacy and offers no authentication hook for
push).

### Discovery (refs advertisement)

- Client: `GET $GIT_URL/info/refs?service=git-upload-pack` (fetch/clone) or
  `?service=git-receive-pack` (push). Exactly one query parameter.
- Smart server MUST reply with:
  - `Content-Type: application/x-$servicename-advertisement`
  - `Cache-Control: no-cache`
  - Body: pkt-line stream starting with `# service=$servicename` + flush
    packet (`0000`), followed by the ref list; capabilities are declared after
    a NUL on the first ref line; terminated by flush `0000`.
- Unknown or disabled service → **403 Forbidden**. Missing repository →
  **404**. Access denied → **403**.
- The client validates the first five bytes against `^[0-9a-f]{4}#` and falls
  back to dumb protocol otherwise — so a smart-only server must ensure the
  advertisement is byte-exact.
- Protocol v2 is requested by the client via the `Git-Protocol: version=2`
  HTTP header on the discovery request.

### Service execution

- Client: `POST $GIT_URL/git-upload-pack` with
  `Content-Type: application/x-git-upload-pack-request`;
  response `application/x-git-upload-pack-result`
  (`git-receive-pack` analogously).
- The HTTP protocol is **stateless**; servers MUST NOT require cookies, and
  clients keep all state. This means our handlers can run the official
  `git-upload-pack` / `git-receive-pack` processes per request with no
  session affinity.
- Requests/responses MUST NOT be cached (`Cache-Control: no-cache`).
- Authentication: standard HTTP (Basic) is expected by clients; over TLS this
  is acceptable, and token auth maps onto it naturally (spec §8).

### Implications for GitHost F1 (Smart HTTP)

1. Auth + authorize **before** invoking any git service: anonymous read only
   for public repos; authenticated push for writers.
2. Bridge the `Git-Protocol` request header into the `GIT_PROTOCOL` env var of
   the spawned `git-upload-pack`/`git-receive-pack` process so protocol v2
   works for HTTP clients.
3. Serve the two endpoints with exact content types and no-cache headers;
   respond 403/404 per the spec above.
4. Delegate the actual pack negotiation entirely to the official git
   executable (spec §7 — no protocol reimplementation).

## 2. Protocol v2

- Client opts in with `version=2`:
  - git:// → extra parameter `\0\0version=2\0`
  - ssh:// and file:// → `GIT_PROTOCOL` environment variable
  - http(s):// → `Git-Protocol` header.
- Server replies with `version 2` + capability advertisement, then serves
  **commands**: `ls-refs` (ref discovery on demand), `fetch` (pack
  negotiation), `object-info` (sizes), plus capabilities like `agent`,
  `object-format` (SHA-256 support), `session-id`, `server-option`.
- v2 is **stateless by default** — designed for HTTP; a flush-pkt (`0000`)
  ends a response; `0002` (response-end) closes stateless responses.
- For SSH, the server side must explicitly **allow** the `GIT_PROTOCOL`
  environment variable to reach the spawned process (our F1 SSH server
  controls env explicitly, so this is a allowlist entry).

## 3. SSH transport

- Client runs `ssh://git@githost.example:2222/user/project.git`; the SSH
  client issues an **exec** request: `git-upload-pack '<path>'` (fetch) or
  `git-receive-pack '<path>'` (push).
- A restricted GitHost SSH server therefore must:
  1. Authenticate by public key only (map key → user, spec §9).
  2. Accept **exec** session requests, reject shell/PTY/forwarding outright.
  3. Parse the command with a strict grammar: exactly
     (`git-upload-pack`|`git-receive-pack`) `<repo-path>` — nothing else.
  4. Enforce read/write authorization per repo *before* starting the process.
  5. Spawn the official git binary with a controlled environment
     (working dir, `GIT_PROTOCOL` passthrough when the client requested v2,
     no user-controlled config).
  6. Log the operation (who/what/result) for the audit trail.
- Host keys: generated at `init`, persisted under the data directory,
  rotatable via config; client-known-hosts migration is a documented
  operation.

## 4. Server-side hooks (push path)

`git-receive-pack` triggers server hooks (`pre-receive`, `update`,
`post-receive`) from the repository's `hooks/` directory. Consequences:

- Hooks must be **installed by GitHost** into the bare repo at creation —
  never derived from pushed content (spec §7.3 forbids trusting hooks from
  repositories).
- The `pre-receive` hook is the natural enforcement point for branch
  protection rules (F4) and size limits.
- Hooks will communicate back to the platform (e.g., via a helper binary with
  a narrow interface), design deferred to F1/F4 ADRs.

## 5. Storage model consequences

- Repositories live on disk as **bare repos** addressed by internal IDs
  (spec §11); path mapping owner/name → ID comes from SQLite, never from the
  URL directly (path-traversal defense).
- All object data stays in standard Git format — `git fsck` remains the
  integrity check, and standard clients interoperate without adaptation
  (spec §7.2).

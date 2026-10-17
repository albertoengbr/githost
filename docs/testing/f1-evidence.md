# F1 evidence record

Environment: Windows 11 (windows/amd64), Go 1.27.1, Git 2.56.0.windows.1,
OpenSSH_for_Windows 9.5p2. All commands executed 2026-10-17.

## 1. Automated suites

```text
$ go test ./...
ok  githost.dev/githost/internal/app
ok  githost.dev/githost/internal/auth        (argon2id, tokens, ssh keys)
ok  githost.dev/githost/internal/cli          (admin/repo commands)
ok  githost.dev/githost/internal/config
ok  githost.dev/githost/internal/database     (migrations 0001+0002)
ok  githost.dev/githost/internal/git           (executor allowlist, env jail)
ok  githost.dev/githost/internal/httpserver
ok  githost.dev/githost/internal/logging
ok  githost.dev/githost/internal/permissions   (authz model incl. public-floor bug fixed)
ok  githost.dev/githost/internal/reposervice   (create compensation, delete)
ok  githost.dev/githost/internal/security
ok  githost.dev/githost/internal/storage       (uuid-addressed bare store)
ok  githost.dev/githost/internal/store         (users/tokens/keys/repos/audit)
ok  githost.dev/githost/tests/e2e               (see §2)
```

## 2. E2E with real git clients (tests/e2e)

### 2.1 Smart HTTP (TestHTTPSClonePushPull + companions)

Verified, in order, with the real `git` client against a live server:

1. Anonymous clone of private repo → 401 challenge, client fails
   ("could not read Username" with prompts disabled).
2. Anonymous clone of nonexistent repo → 401 (no existence leak).
3. Anonymous clone of public repo → success, with and without `.git`.
4. Owner token clone of private repo → success.
5. Commit + push → success; server-side `git fsck --strict` clean.
6. Second clone + push + pull in first clone → merge verified on disk.
7. Read-only member: clone succeeds, push rejected; the rejected push
   did NOT reach the repository (verified via server-side log).
8. Stranger with valid token → 404 (existence hidden).
9. Read-scope member push → 403.
10. Anonymous push to public repo → 401.
11. Protocol v2 clone (`-c protocol.version=2`) → success.
12. Archived repository: push → 403 "repository is archived"; fetch → OK.

### 2.2 Path traversal (TestHTTPSPathTraversalProbes)

- Raw dot-segment requests (`/a/./../b/...`, `%2e%2e`, `/../`,
  `/etc/passwd` shapes) sent with a non-normalizing HTTP client → **400**
  from `httpgit.Guard` (verified: Go's client sends them raw; libcurl
  normalizes before sending, which the test documents).
- Client-normalized traversal grants nothing beyond the clean URL
  (stranger probe fails with 404-equivalent; owner probe reaches own
  repo — same as clean URL).
- Repositories root after all probes: exactly the one legitimate
  `<uuid>.git` directory.

### 2.3 SSH transport (TestSSHClonePushPull)

With a real `ssh` client (native Windows OpenSSH 9.5):

1. Unregistered key → "Permission denied (publickey)".
2. Registered key → clone of private repo succeeds.
3. Commit + push over SSH → success; second clone + push + pull
   verified; server-side `git fsck --strict` clean.
4. `ssh ... whoami` (shell) → refused: "interactive sessions are
   disabled"; exit != 0.
5. `ssh ... "rm -rf /"` (arbitrary exec) → refused by the command
   grammar.
6. Stranger's registered key on private repo → "repository not found"
   (existence hidden, same policy as HTTP).
7. Public repository readable by any authenticated user; the probe
   captured the actual upload-pack advertisement (`... capabilities^{}`
   with `agent=git/2.56.0...`).
8. Read-only member: clone OK, push → "permission denied"; server-side
   log shows the rejected push never landed.

Platform quirks discovered and documented by this work (all in code
comments): `GIT_CONFIG_GLOBAL=NUL` breaks Git-for-Windows (use
`/dev/null`); native Windows `ssh` rejects `StrictModes` as a CLI option
and requires icacls-restricted key files; `GIT_SSH_COMMAND` paths must
use forward slashes (git tokenizes it without a shell); `ssh host:port`
is invalid syntax (port goes via `-p`).

## 3. Live binary validation (real CLI, both transports)

```text
$ githost init && githost admin create-user --username alice --password-stdin
created user alice (id=1, admin=true)
$ githost admin create-token --username alice --scope repo:read --scope repo:write
token for alice (scopes: repo:read repo:write): ght_***  (shown once)
$ githost repo create --owner alice --name proj
created alice/proj (private, uuid e4f55499813bc754c6916f74c2380910)
$ githost admin add-ssh-key --username alice --key-file id_ed25519.pub
added key SHA256:1f9JGIIox0A/OApUtLC2R7nqFVynXxLdowsGWCF0wqI for alice
$ githost serve         (ssh.enabled=true, port 2222)
{"msg":"ssh_server_started","addr":"[::]:2222"}
{"msg":"http_server_started","addr":"127.0.0.1:3000"}

HTTPS: git clone http://alice:ght_***@127.0.0.1:3000/alice/proj.git
       commit -> push: " * [new branch] main -> main"
SSH:   git clone ssh://git@127.0.0.1:2222/alice/proj.git
       commit -> push: " 4d0a810..b27806d  main -> main"
Cross: git pull over HTTPS brings in the SSH-pushed commit
       ("ssh-file.txt | 1 +", fast-forward)
```

Server logs captured the whole lifecycle in structured JSON, including
the anonymous 401 → authenticated 200 transition on
`/alice/proj.git/info/refs` and the `git-upload-pack` POSTs.

## 4. Security-relevant behaviors proven by tests

| Threat (docs/security/threat-model.md) | Test evidence |
|---|---|
| T1 command injection | executor allowlist unit tests; no shell anywhere; SSH command grammar rejects `rm -rf /` |
| T2 path traversal | storage.Path validation; httpgit.Guard 400s; SSH splitRepoPath; e2e probes |
| T3 private repo exposure | anonymous → 401; stranger → 404; negative git client tests over both transports |
| T4 SSH shell escape | raw ssh probes refused; only session channels; no password auth |
| T7 resource exhaustion | every executor call has a timeout (DefaultTimeout 10 min) |
| T8 env injection | env allowlist unit test (GIT_PROTOCOL only, `version=N` shape); host config neutralization test |

## 5. F1 acceptance criteria — verdict

| # | Criterion (roadmap F1) | Verdict |
|---|---|---|
| Create a repository in GitHost | PASS (CLI + service, private by default) |
| Clone over HTTPS | PASS (e2e §2.1, live §3) |
| Clone over SSH | PASS (e2e §2.3, live §3) |
| Commits, push and pull preserve data fully | PASS (`git fsck --strict` clean server-side after every flow; cross-transport pull verified) |
| Authorization enforced on both transports | PASS (negative tests: anonymous, stranger, read-only, scope, archived) |

**F1 is ACCEPTED.** Race-detector runs remain gated on a POSIX/CI runner
(unchanged limitation from F0).

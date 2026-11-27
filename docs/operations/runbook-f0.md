# Runbook — F0–F3 operations

How to install, run and operate a GitHost instance. Update this file with
every phase.

> New to GitHost? The [first-access tutorial](../getting-started.md) walks
> through installation, admin creation and the first login step by step.

## Requirements

- Windows/Linux/macOS/BSD on amd64 (arm64 builds exist; see
  compatibility matrix)
- Go ≥ 1.26 (only when building from source)
- Git ≥ 2.30 on the server host (required since F1)
- An SSH client (for the SSH transport) — any modern OpenSSH works

## Install from source

```console
$ git clone <your-githost-repo> githost
$ cd githost
$ go build -trimpath -o githost ./cmd/githost
```

Or use the scripts: `scripts\verify.ps1` (Windows) / `scripts/verify.sh`
(POSIX) for a full check, `scripts\cross-compile*` for the OS/arch
matrix into `dist/`.

## First run

```console
$ ./githost init          # config template + data/ (secret, database,
                          # repositories/ and ssh/ host key when enabled)
$ $EDITOR githost.yaml    # review address/ports; set server.ssh.enabled
                          # to true for the SSH transport (port 2222)
$ ./githost serve
githost ... listening on http://127.0.0.1:3000
```

`serve` applies pending migrations on every start (idempotent) and shuts
down cleanly on Ctrl+C / SIGTERM (up to 10s drain; in-flight SSH git
sessions finish on their own).

## User, token and key administration

Public registration is disabled by design; the operator creates
accounts locally:

```console
$ ./githost admin create-user --username alice --password-stdin   # first user = admin
$ ./githost admin set-password --username alice --password-stdin  # ends sessions + revokes tokens
$ ./githost admin create-token --username alice --scope repo:read --scope repo:write
$ ./githost admin add-ssh-key --username alice --key-file ~/.ssh/id_ed25519.pub
$ ./githost admin list-users
```

Tokens are shown exactly once and stored only as SHA-256 hashes.
Scopes: `repo:read` (fetch/clone), `repo:write` (push), `admin`.

## Repositories

```console
$ ./githost repo create --owner alice --name myproject            # private by default
$ ./githost repo create --owner alice --name opensource --visibility public
$ ./githost repo add-member --owner alice --name myproject --user bob --permission write
$ ./githost repo list [--owner alice]
$ ./githost repo delete --owner alice --name myproject --confirm
```

Member permissions: `read`, `write`, `admin`. Owners and instance
admins always have full access. Archived repositories (service-level
today, UI in F2) reject pushes but keep serving reads.

## Using Git

HTTPS (token as password — put the instance behind a TLS reverse
proxy for production):

```console
$ git clone http://alice:<token>@githost.example/alice/myproject.git
$ git push origin main
```

SSH (key must be registered via `admin add-ssh-key`):

```console
$ git clone ssh://git@githost.example:2222/alice/myproject.git
$ git push origin main
```

The SSH endpoint only accepts `git-upload-pack`/`git-receive-pack`;
interactive sessions are refused and logged.

## Using the web UI

Browse to the server root (`http://githost.example:3000/`):

- **Log in** with your account (sessions last 30 days; login attempts
  are rate-limited).
- **New repository** creates a private-by-default repository.
- Repository pages: file browser, raw download, commit history with
  patches, branches, tags; settings pages manage visibility, default
  branch, rename, archive and deletion.
- **Settings → SSH keys** and **Settings → Tokens** manage personal
  credentials. Tokens are shown exactly once at creation.
- Anonymous visitors see public repositories only; private ones
  answer 404.

## Using the REST API

```console
$ curl -H "Authorization: token ght_..." http://githost.example:3000/api/v1/user
$ curl -H "Authorization: token ght_..." \
       -d '{"name":"from-api","visibility":"private"}' \
       http://githost.example:3000/api/v1/repos
```

The full contract is [docs/api/openapi.yaml](../api/openapi.yaml)
(bearer form also accepted). Scopes: `repo:read` for queries,
`repo:write` for changes, `admin` for instance-wide listings.

## Health and diagnostics

```console
$ curl -s http://127.0.0.1:3000/healthz   # {"status":"ok","database":"ok",...}
$ ./githost doctor                       # config, data dir, secret, git,
                                         # database/migrations, port checks
$ ./githost version
```

Doctor exit codes: 0 all critical checks pass; 1 something failed
(warnings like an occupied port do not fail the run).

## Data layout

```text
githost.yaml            config (edit freely; init never overwrites it)
data/
  githost.db            SQLite metadata (+ -wal/-shm side files, normal)
  secret.key            instance secret — keep private, never commit
  repositories/<uuid>.git   bare repositories (do not edit by hand)
  ssh/host_ed25519      SSH host key (regenerate to force clients to
                        re-verify the host)
```

Back up `githost.yaml` + the whole `data/` directory after stopping the
server (proper managed backup/restore arrives in F3). Cloning the
repositories directory with git itself is also a valid data export.

## Troubleshooting

| Symptom | Diagnosis |
|---|---|
| `hint: run "githost init" first` | No config file at the `--config` path |
| `listen on ...: bind: ...` | Port occupied — `doctor` reports it; change `server.http_port` |
| healthz 503 | Database unreachable: check disk, file permissions, data path |
| `database is dirty: applied version NNNN is not present` | Binary older than the schema — run a matching or newer build; never downgrade |
| `Permission denied (publickey)` over SSH | Key not registered (`admin add-ssh-key`), or wrong key offered |
| 404 with a valid token | Repository does not exist **or** you have no access (same answer on purpose) |
| Push to public repo asks for credentials | Anonymous write is never allowed — use a `repo:write` token |

## Backup and restore (F3)

Backups are single tar.gz files containing a manifest with the SHA-256
of every file, a consistent SQLite snapshot (VACUUM INTO), the instance
secret, SSH host keys and all bare repositories. The server must be
stopped — an OS-level instance lock enforces it and refuses otherwise.

```console
$ githost backup create --config githost.yaml          # -> backups/githost-backup-<ts>.tar.gz
$ githost backup validate --archive backups/<file>.tar.gz
$ githost backup list --config githost.yaml
$ githost backup prune --config githost.yaml --keep 7
```

Restoring targets a **clean data directory** (an old instance must be
moved away first — this is deliberate):

```console
$ githost backup restore --archive backups/<file>.tar.gz \
                          --data /var/lib/githost-new \
                          --config-out /etc/githost/githost-restored.yaml
```

Then point (or copy) `githost.yaml` at the restored data directory and
`githost serve`. The acceptance proof for this flow lives in
tests/e2e/backup_test.go: login, tokens, repositories and pushes all
work after a restore.

## Native TLS (F3)

Set `server.tls.enabled: true` with `cert_file`/`key_file` to serve
HTTPS directly (certificates must be readable at startup; the server
fails fast otherwise). With native TLS, session cookies set the Secure
flag automatically. Reverse proxy deployments remain equally supported:
add the proxy's CIDR to `server.trusted_proxies` so GitHost honors
`X-Forwarded-Proto: https` (Secure cookie + HSTS) and `X-Forwarded-For`
(rate-limit/audit client IP). Without that list, both headers are ignored.

## Push limits (F3)

`limits.max_push_bytes` (default 512 MiB) caps accepted packs. git
itself enforces it (receive.maxInputSize) on both transports and the
HTTP layer caps the request body. Raise the limit for repositories with
legitimately large artifacts — the value counts the **compressed**
pack, not the file sizes.

## Security hardening (post-F6)

Four settings in `githost.yaml` are new. All default to the safe value:

```yaml
server:
  trusted_proxies: []          # CIDRs of YOUR reverse proxies only
  max_concurrent_requests: 0   # 0 = no cap; long clones hold a slot
webhooks:
  allowed_cidrs: []            # private targets denied unless listed
```

- **Webhooks**: the secret is mandatory (≥16 chars) and is stored AES-GCM
  sealed with `data/secret.key`; legacy plaintext rows are sealed
  automatically on the next start. Payloads are always signed with
  `X-GitHost-Signature`. Private/loopback/link-local/multicast targets are
  refused at registration and at dial time; add only the internal CIDRs you
  control to `allowed_cidrs` (loopback stays blocked even if listed).
  Redirects are not followed. **Back up `secret.key` with the database** —
  without it, sealed secrets cannot be opened after a restore.
- **Credentials**: changing or resetting a password now ends all sessions
  *and* revokes every access token. Update any automation that expects a
  token to survive a password reset.
- **MFA**: a TOTP code is single-use per 30-second step; replaying the same
  code within its validity window fails. Re-enabling MFA resets the mark.
- **Throttling**: login/MFA guessing is limited per source IP *and* per
  account; SSH key authentication is throttled per IP. Unknown usernames
  cost the same Argon2 work as real ones, so login timing does not
  enumerate accounts.
- **Backpressure**: set `server.max_concurrent_requests` to a positive
  value on an exposed instance; excess requests get 503 instead of
  consuming resources. Size it for peak concurrent clones/pushes.
- **Retention**: the runner handles `prune_audit_events` and
  `prune_webhook_deliveries` (90 days by default, job detail = days), but
  there is no CLI/scheduler that enqueues them yet — the same is true of
  the older `prune_sessions`/`rotate_backups` kinds. Until that follow-up
  lands, issue the bounded `DELETE ... WHERE created_at < ?` statements
  yourself during maintenance; do not assume automatic scheduling.
- **Supply chain**: CI runs `govulncheck` (currently informative) and
  Dependabot proposes weekly Go/Action updates.

The full negative-test matrix is in
[docs/testing/security-evidence.md](../testing/security-evidence.md).

## Service manager integration

Concrete unit files ship in `packaging/`: hardened systemd unit
(+install README), macOS launchd plist, FreeBSD rc.d script, and a
Windows guide (NSSM/WinSW/Task Scheduler — a first-party Windows SCM
handler is a documented gap). The Docker image lives in
`packaging/docker/Dockerfile` — remember to set
`server.address: "0.0.0.0"` in the container config when publishing
ports.

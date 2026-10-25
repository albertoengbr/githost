# Getting started: first access

This tutorial walks through a complete first run on Debian/Ubuntu: download and
verify a release, initialize the instance, create the administrator account,
log in, push a first repository and secure the admin password.

> Building from source works the same way after
> `go build -trimpath -o githost ./cmd/githost`; skip section 1 in that case.

## Requirements

- Debian 12+ / Ubuntu 22.04+ (any supported Linux works the same way)
- Git **2.30 or newer** on the server
- amd64 or arm64
- `curl`, `tar` and `sha256sum` (standard on Debian)

```console
$ sudo apt update && sudo apt install -y git curl
$ git --version
```

## 1. Download and verify a release

```console
$ VERSION=0.1.0   # replace with the latest release
$ curl -LO "https://github.com/albertoengbr/githost/releases/download/v${VERSION}/githost_${VERSION}_linux_amd64.tar.gz"
$ curl -LO "https://github.com/albertoengbr/githost/releases/download/v${VERSION}/SHA256SUMS.txt"
$ sha256sum --check --ignore-missing SHA256SUMS.txt
$ tar -xzf "githost_${VERSION}_linux_amd64.tar.gz"
$ sudo install -m 0755 githost /usr/local/bin/githost
$ githost version
```

Replace `amd64` with `arm64` on ARM hosts. If `sha256sum` reports `FAILED`, do
not continue — download the archive again.

## 2. First run (foreground)

For a first look, run everything as your own user:

```console
$ mkdir -p ~/githost && cd ~/githost
$ githost init
$ githost admin create-user --username admin --admin --password-stdin
```

At the password prompt, type the walkthrough password and press Enter.

### Administrator credentials

| Field    | Value          |
|----------|----------------|
| Username | `admin`        |
| Password | `GitHost2026!` |

> **Security note:** `GitHost2026!` is the walkthrough credential and is
> published in this repository. It is acceptable for a localhost test instance
> only. **Change it before exposing the server to a network** (section 6).

Start the server:

```console
$ githost serve
githost 0.1.0 listening on http://127.0.0.1:3000
```

Leave it running and continue in another terminal, or stop it with `Ctrl+C`
and use the systemd setup below for a permanent installation.

## 3. Production install with systemd

### 3.1 Service user and directories

```console
$ sudo useradd --system --home /var/lib/githost --shell /usr/sbin/nologin githost
$ sudo install -d -o githost -g githost /var/lib/githost
$ sudo install -d -o githost -g githost /etc/githost
```

`/etc/githost` belongs to the service user because `init` writes the
configuration template there. The systemd unit mounts `/etc` read-only for the
running server (`ProtectSystem=strict`), so it cannot rewrite its own config at
runtime.

### 3.2 Initialize and move the data directory

```console
$ sudo -u githost githost init --config /etc/githost/githost.yaml
$ sudoedit /etc/githost/githost.yaml
```

Set at least:

```yaml
server:
  # Keep loopback and put a TLS reverse proxy in front (recommended), or
  # use "0.0.0.0" only on a trusted network.
  address: "127.0.0.1"
  http_port: 3000

paths:
  data: "/var/lib/githost"
```

`init` never overwrites an existing configuration, so run it again with the
edited file to create the instance at the final path, then remove the
first-run leftover:

```console
$ sudo -u githost githost init --config /etc/githost/githost.yaml
$ sudo rm -rf /etc/githost/data
```

### 3.3 Create the administrator

```console
$ sudo -u githost githost admin create-user \
    --config /etc/githost/githost.yaml \
    --username admin --admin --password-stdin
```

Use the walkthrough credentials (`admin` / `GitHost2026!`) or a strong password
of your own. Running the command as `githost` is required so it can write to
`/var/lib/githost`. The first account on a fresh instance is an administrator
even without `--admin`; the flag keeps that explicit.

Check the result:

```console
$ sudo -u githost githost admin list-users --config /etc/githost/githost.yaml
ID  USERNAME  ADMIN
1   admin     true
```

### 3.4 Install the systemd unit

The hardened unit ships in the repository (it is not part of the release
archive):

```console
$ curl -fsSLO https://raw.githubusercontent.com/albertoengbr/githost/main/packaging/systemd/githost.service
$ sudo install -m 0644 githost.service /etc/systemd/system/githost.service
$ sudo systemctl daemon-reload
$ sudo systemctl enable --now githost
$ sudo systemctl status githost --no-pager
$ curl -s http://127.0.0.1:3000/healthz
{"database":"ok","status":"ok","version":"0.1.0"}
```

Logs: `journalctl -u githost -f`.

### 3.5 Reach the web interface

- **Same machine:** <http://127.0.0.1:3000>.
- **Another machine on a trusted LAN:** set `server.address: "0.0.0.0"`, open
  the firewall for the port (`sudo ufw allow 3000/tcp`) and browse to
  `http://SERVER-IP:3000`.
- **Internet-facing:** keep `127.0.0.1` and put a TLS reverse proxy (Caddy,
  nginx) in front, or enable native TLS in `githost.yaml`
  (`server.tls.enabled`, `cert_file`, `key_file`).

## 4. First login

Open `/login` on your instance (for example
<http://127.0.0.1:3000/login>) and sign in with:

| Field    | Value          |
|----------|----------------|
| Username | `admin`        |
| Password | `GitHost2026!` |

After signing in you land on the dashboard. The top bar shows the repository
search; the user menu (top right) offers **New repository**, **Users** (admins
only), **SSH keys**, **Tokens**, **Password** and **Two-factor**.

If the login is rejected with *"Invalid username or password"* on a fresh
installation, the account was not created — repeat section 2 or 3.3. Public
registration is disabled by design.

## 5. First repository and first push

### 5.1 Create the repository

1. Open the user menu → **New repository**.
2. Name it `hello`, choose `Private` (default) or `Public`, keep the default
   branch `main` and submit.
3. The empty repository page shows the clone URL and the *"No commits yet"*
   onboarding.

### 5.2 Create an access token

1. User menu → **Tokens** → create a token named `laptop`.
2. Select the `repo:read` and `repo:write` scopes.
3. Copy the `ght_...` value — it is shown **exactly once**.

### 5.3 Push a first commit over HTTPS

```console
$ git clone http://admin:ght_YOUR_TOKEN@127.0.0.1:3000/admin/hello.git
$ cd hello
$ printf '# Hello\n\nFirst repository hosted on GitHost.\n' > README.md
$ git add README.md
$ git commit -m "Initial commit"
$ git push -u origin main
```

Refresh the repository page: the README is rendered and the commit appears in
the history. Anonymous write is never allowed, so HTTPS pushes always require
a `repo:write` token as the password.

### 5.4 SSH (optional)

Enable the SSH transport in `githost.yaml`:

```yaml
server:
  ssh:
    enabled: true
    address: "0.0.0.0"
    port: 2222
```

Restart the service, add your public key under **SSH keys** (or with
`githost admin add-ssh-key`), then:

```console
$ git clone ssh://git@SERVER:2222/admin/hello.git
```

The SSH endpoint is exec-only: it accepts `git-upload-pack` and
`git-receive-pack` and refuses interactive shells.

## 6. Change the admin password

The walkthrough password is public, so change it on any non-local instance.

From the web interface:

1. User menu → **Password**.
2. Enter the current password (`GitHost2026!`), then the new one twice.
3. Submit. **Every session of the account is invalidated** — sign in again with
   the new password.

From the server shell, for example when the password is lost:

```console
$ sudo -u githost githost admin set-password \
    --config /etc/githost/githost.yaml \
    --username admin --password-stdin
```

Type the new password when prompted (policy: at least 8 characters). The
command also invalidates every session of the account. Administrators can reset
another account's password under **Users → Edit → Reset password**.

## 7. Troubleshooting first access

| Symptom | Cause and fix |
|---|---|
| *"Invalid username or password"* right after installation | No account exists yet. Create it: `githost admin create-user --username admin --admin --password-stdin`. The first account is an admin. |
| *"The form expired. Please try again."* on login | The CSRF cookie was not kept. Use one hostname consistently (no mix of `localhost` and `127.0.0.1`) and allow cookies. |
| Page unreachable from another machine | `server.address` is `127.0.0.1` by default. Use a TLS reverse proxy or bind `0.0.0.0` on a trusted network, and open the firewall. |
| `systemctl status` shows permission errors on the database | Run `init`/`admin` commands as the service user with the same config: `sudo -u githost githost ... --config /etc/githost/githost.yaml`. |
| Account exists but the password is lost | Reset it from the server shell: `githost admin set-password --username admin --password-stdin` (add `--config` when not in the instance directory). All sessions are invalidated. |
| `database is dirty: applied version NNNN is not present` | The binary is older than the schema. Use the same or a newer release; never downgrade in place. |
| Login succeeds but every page redirects to `/login` | Cookies are blocked or the instance is reached through two different origins. Use a single origin (the proxy's public hostname). |
| Push asks for a password again | Use an access token with `repo:write` as the password, not the account password. |

More operational details: [operations/runbook-f0.md](operations/runbook-f0.md).

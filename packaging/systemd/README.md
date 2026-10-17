# systemd installation

1. Copy the `githost` binary to `/usr/local/bin/githost`.
2. Create the service user and directories:

```console
# useradd --system --home /var/lib/githost --shell /usr/sbin/nologin githost
# mkdir -p /etc/githost /var/lib/githost
# chown githost:githost /var/lib/githost
```

3. Initialize the instance:

```console
# sudo -u githost githost init --config /etc/githost/githost.yaml
# edit /etc/githost/githost.yaml   # set data: /var/lib/githost and ports
```

4. Install the unit:

```console
# cp githost.service /etc/systemd/system/
# systemctl daemon-reload
# systemctl enable --now githost
```

Logs go to the journal: `journalctl -u githost -f`.

Backups: stop the unit first (`systemctl stop githost`), run
`githost backup create --config /etc/githost/githost.yaml`, then start it
again — the instance lock enforces this ordering.

## Upgrading

1. `systemctl stop githost`
2. Replace `/usr/local/bin/githost` with the new binary.
3. `systemctl start githost` (migrations run automatically).
4. Rollback = restore the previous binary + `githost backup restore` into
   a clean data directory, then point the config at it.

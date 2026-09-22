# Natbox

Natbox is a standalone, small management service for Alpine NAT containers on a single VPS. It uses Incus or LXD/LXC and keeps the management API separate from 3x-ui. The runtime is selected with `NATBOX_RUNTIME`; when unset, Natbox prefers `incus` and falls back to `lxc`.

The LXD path has been smoke-tested on Ubuntu 24.04 with LXD 5.21.7, a `lxdbr0` NAT bridge, and an Alpine amd64 image. LXD image names should use the remote prefix, for example `images:alpine/3.24`.

## Requirements

- Linux VPS with Incus installed and initialized
- Incus bridge with outbound NAT, usually `incusbr0`
- Go 1.22+ for building from source

## Run

```bash
sudo apt update
sudo apt install -y incus
sudo incus admin init
go build -o natbox .
NATBOX_LISTEN=127.0.0.1:8787 ./natbox
```

For an amd64 Linux VPS, build from another platform with:

```bash
GOOS=linux GOARCH=amd64 go build -o natbox .
GOOS=linux GOARCH=amd64 go build -o natbox-hash ./cmd/natbox-hash
```

On Windows, use `.\build-linux.ps1` instead. It forces `GOOS=linux`, `GOARCH=amd64`,
`CGO_ENABLED=0`, disables an unrelated parent `go.work`, runs the test and vet
gates, and writes `SHA256SUMS` beside the two Linux artifacts.

Open `http://127.0.0.1:8787` through an SSH tunnel:

```bash
ssh -L 8787:127.0.0.1:8787 root@your-vps
```

The default listener is loopback. If you bind to a non-loopback address, Natbox refuses to start unless `NATBOX_TOKEN` is at least 16 characters. The browser asks for that Bearer token once per session.

Install as a systemd service:

```bash
sudo ./install.sh
sudo nano /etc/natbox/natbox.env
sudo systemctl restart natbox
```

Natbox supports container creation, batch creation with duplicate-name preflight, host capacity preflight, reusable templates, persistent SQLite management metadata and audit events, memory/disk/CPU limits, boot autostart, start/stop/restart/delete, IPv4 and resource inspection, real per-instance network counters, Alpine OpenSSH setup from an authorized public key, TCP/UDP port-forward management, configurable public port policy, and automatic SSH forwarding in the `2201-2299` range. New containers wait for a real IPv4 address before the create request is reported successful. The host's single public IP is shared; each public listen port can belong to only one container.

Important API paths:

New integrations should use the stable `/api/v1/...` prefix. The original
`/api/...` paths remain available for existing clients and the browser UI.
Error responses contain a stable machine-readable `code` plus a human-readable
`message`; clients should branch on `code`, not translated message text.

```text
GET  /api/containers
GET  /api/managed-containers   persistent Natbox declarations
GET  /api/audit                paginated safe operation audit events
GET  /api/backup               export managed declarations as JSON
POST /api/backup/restore       {\"confirm\":\"RESTORE\",\"backup\":{...}}; replaces metadata only
GET  /api/reconcile            compare managed declarations with live runtime
GET  /api/host                  host memory/disk capacity and 120MB/1GB estimate
GET  /api/templates             built-in Alpine presets
GET  /api/ports                 configured ranges and globally used public ports
POST /api/containers/bulk       {"prefix":"nat","startIndex":1,"count":2,"image":"images:alpine/3.24","memoryLimitMb":120,"diskLimitGb":1,"cpuLimit":"12%","allocateSSH":true}
GET  /api/containers/{name}/info
POST /api/containers/{name}/ssh-key   {"publicKey":"ssh-ed25519 ..."}
POST /api/containers/{name}/ssh        automatic TCP port -> container 22
POST /api/containers/{name}/restart   restart and wait for IPv4
GET  /api/containers/{name}/policy    read quota/expiry policy and usage
POST /api/containers/{name}/policy    {"quotaBytes":0,"expiresAt":""}; counters reset at policy update
```

SSH setup installs `openssh-server` inside the Alpine instance, writes the supplied key to root's `authorized_keys`, and enables `sshd`. The container must be running and have outbound network access when the setup is requested.

Capacity preflight reserves 256 MiB RAM and 1 GiB disk by default so the host and management service retain headroom. Override with `NATBOX_MEMORY_RESERVE_MB` and `NATBOX_DISK_RESERVE_GB`. The displayed network counters come from the runtime state API and are cumulative since the instance/runtime counter reset; Natbox does not claim to provide historical billing or quota accounting.

The SQLite database defaults to `/var/lib/natbox/natbox.db` on Linux and can be changed with `NATBOX_DB`. It stores desired container metadata and safe audit records, not passwords, SSH private keys, public-key contents, access tokens, or runtime counters. Existing LXD containers created before metadata persistence are still visible in `/api/containers` but are not automatically fabricated into managed records.

Administrator login is optional. Run `/opt/natbox/natbox-hash` interactively to generate an Argon2id hash, then set `NATBOX_ADMIN_PASSWORD_HASH` in `/etc/natbox/natbox.env` and restart the service. When enabled, browser mutations require an HttpOnly session cookie plus CSRF header. Login routes are rate-limited in memory and login/logout events are audited. Existing Bearer token access remains supported for automation and can bypass browser CSRF checks; keep that token secret and prefer loopback or HTTPS.

For automatic database snapshots, set `NATBOX_BACKUP_DIR` (for example `/var/lib/natbox/backups`). Natbox writes SQLite-consistent `0600` snapshots, keeps `NATBOX_BACKUP_RETENTION` files (default 7), and records success/failure in the audit log. Backups are local; copy them to separate storage for disaster recovery. The service uses a restrictive `UMask`, increased file descriptor limit, and graceful SIGTERM shutdown.

Backups contain only Natbox managed declarations. Restore is intentionally metadata-only: it does not create, delete, start, stop, or reconfigure LXD instances. Use `/api/reconcile` after restore to identify managed records missing from the runtime, runtime instances not managed by Natbox, and desired-state drift. Audit history is not overwritten by restore.

Quota enforcement runs once per minute. `quotaBytes=0` disables the traffic limit and an empty `expiresAt` disables expiry. A policy update records current runtime receive/send counters as the baseline, so earlier traffic is not charged to the new policy. When a limit is reached, Natbox stops the container, changes its desired state to `stopped`, and writes an audit event; it never deletes the instance.

For direct HTTPS, set both `NATBOX_TLS_CERT` and `NATBOX_TLS_KEY` in `/etc/natbox/natbox.env`. Non-loopback listeners still require `NATBOX_TOKEN` with at least 16 characters. A reverse proxy with its own certificate and access control remains the preferred public deployment.

Production operations
---------------------

The service exposes `GET /api/metrics` in Prometheus text format and `GET /api/diagnostics` for runtime, host, and reconciliation checks. Both follow the normal Natbox authentication policy. `POST /api/reconcile/repair` repairs only desired-state drift for managed containers; unmanaged and missing instances are reported but left untouched.

`POST /api/containers/bulk/action` accepts `{"names":["nat01","nat02"],"action":"restart"}`. Supported actions are `start`, `stop`, `restart`, and `delete`. Each item is audited independently and the response includes per-item errors.

Set `NATBOX_REQUIRE_TLS=1` to refuse a plaintext listener. This requires `NATBOX_TLS_CERT` and `NATBOX_TLS_KEY`. Natbox also sends browser security headers, and the systemd unit applies filesystem, privilege, and address-family restrictions. Review `natbox.service` if your runtime needs additional capabilities.

The repository includes `openapi.yaml`, a CI workflow for tests/vet and Linux amd64/arm64 builds, and a tag-triggered release workflow. Release artifacts include `natbox`, `natbox-hash`, and `SHA256SUMS`.

Public deployment and upgrades
------------------------------

The repository is intended to be public, but deployment secrets remain local.
Do not commit `.env` files, administrator password hashes, bearer tokens, SSH
keys, SQLite databases, backups, or server-specific addresses. See
`SECURITY.md` before opening an issue.

For a fresh Linux VPS, the simplest installation is:

```bash
curl -fsSL https://raw.githubusercontent.com/csy6666/natbox/main/install-online.sh | sudo sh
```

The command detects amd64/arm64, downloads the latest stable release, verifies
both binaries with `SHA256SUMS`, installs the systemd unit, creates a protected
default environment file, and checks the local health endpoint. To pin a
version instead of using the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/csy6666/natbox/main/install-online.sh | sudo NATBOX_VERSION=v0.1.1 sh
```

For maximum reviewability, download the script first, inspect it, then run
`sudo sh install-online.sh`.

The installer intentionally checks that `systemd` and a usable Incus/LXD
runtime already exist. It does not run `incus admin init` automatically because
storage pools and bridge/NAT choices can change the VPS network. After a
runtime-side failure during container creation, Natbox attempts to delete the
new instance; batch creation rolls back all instances created by that request
and records cleanup failures in the audit log.

Reconciliation also reports unreadable or duplicate public port forwards under
`portForwardIssues`. It does not delete or rewrite port devices automatically.

Install a locally built release on a Linux VPS:

```bash
sudo ./install.sh
```

The installer replaces binaries atomically, preserves `/etc/natbox/natbox.env`,
and fails if the systemd service does not become active. After publishing a
GitHub Release, upgrade an existing installation with:

```bash
sudo NATBOX_VERSION=1.0.0 ./upgrade.sh
```

Leaving `NATBOX_VERSION` unset downloads the latest release. The script selects
amd64 or arm64, verifies `SHA256SUMS`, backs up the current binary, restarts the
service, checks `/api/health`, and restores the previous binary if startup or
health verification fails. It does not modify the SQLite database or the
Natbox environment file.

To publish a release, create and push a tag such as `v1.0.0`; GitHub Actions
builds Linux amd64/arm64 artifacts and creates the release automatically.

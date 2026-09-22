#!/bin/sh
set -eu

install_dir=/opt/natbox
service_file=/etc/systemd/system/natbox.service
state_dir=/var/lib/natbox

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd/systemctl is required" >&2
  exit 1
fi
runtime=${NATBOX_RUNTIME:-}
if [ -z "$runtime" ]; then
  if command -v incus >/dev/null 2>&1; then runtime=incus
  elif command -v lxc >/dev/null 2>&1; then runtime=lxc
  fi
fi
if [ -z "$runtime" ] || ! "$runtime" version >/dev/null 2>&1; then
  echo "usable Incus/LXD runtime is required; initialize it before installing Natbox" >&2
  exit 1
fi

mkdir -p "$install_dir" /etc/natbox "$state_dir" "$state_dir/upgrade-backups"

install_atomic() {
  source=$1
  target=$2
  temporary="$target.tmp.$$"
  install -m 0755 "$source" "$temporary"
  mv -f "$temporary" "$target"
}

install_atomic natbox "$install_dir/natbox"
if [ -f natbox-hash ]; then
  install_atomic natbox-hash "$install_dir/natbox-hash"
fi
install -m 0644 natbox.service "$service_file"

if [ ! -f /etc/natbox/natbox.env ]; then
  cat > /etc/natbox/natbox.env <<'EOF'
# Keep loopback unless you put Natbox behind an authenticated reverse proxy.
NATBOX_LISTEN=127.0.0.1:8787
# SQLite management metadata and audit database.
NATBOX_DB=/var/lib/natbox/natbox.db
# Optional administrator login. Generate the hash with: natbox-hash
# NATBOX_ADMIN_PASSWORD_HASH=$argon2id$v=19$m=65536,t=3,p=2$...
# Set NATBOX_COOKIE_SECURE=1 when serving HTTPS.
# NATBOX_COOKIE_SECURE=1
# Set to 1 to refuse plaintext listeners; requires both TLS paths below.
# NATBOX_REQUIRE_TLS=1
# For direct non-loopback access, set a random token of at least 16 characters.
# NATBOX_TOKEN=replace-with-a-long-random-token
# Optional public port policy. The SSH allocator uses the SSH range below.
# NATBOX_PORT_MIN=1
# NATBOX_PORT_MAX=65535
# NATBOX_SSH_PORT_MIN=2201
# NATBOX_SSH_PORT_MAX=2299
# Optional HTTPS. Set both files and keep the listener protected by NATBOX_TOKEN.
# NATBOX_TLS_CERT=/etc/natbox/tls/fullchain.pem
# NATBOX_TLS_KEY=/etc/natbox/tls/privkey.pem
# Capacity reserve defaults: 256 MiB RAM and 1 GiB disk.
# NATBOX_MEMORY_RESERVE_MB=256
# NATBOX_DISK_RESERVE_GB=1
# Optional automatic SQLite snapshots. Retention is the number of files kept.
# NATBOX_BACKUP_DIR=/var/lib/natbox/backups
# NATBOX_BACKUP_INTERVAL_MIN=360
# NATBOX_BACKUP_RETENTION=7
# Optional automatic desired-state repair (disabled by default).
# NATBOX_AUTO_REPAIR=1
# NATBOX_AUTO_REPAIR_INTERVAL_MIN=5
EOF
fi

systemctl daemon-reload
systemctl enable --now natbox.service
if ! systemctl is-active --quiet natbox.service; then
  systemctl --no-pager --full status natbox.service || true
  echo "natbox.service did not become active" >&2
  exit 1
fi
systemctl --no-pager --full status natbox.service

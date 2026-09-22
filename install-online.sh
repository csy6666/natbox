#!/bin/sh
set -eu

repo=${NATBOX_REPO:-csy6666/natbox}
requested_version=${NATBOX_VERSION:-}
install_dir=${NATBOX_INSTALL_DIR:-/opt/natbox}
state_dir=/var/lib/natbox
service_file=/etc/systemd/system/natbox.service
health_url=${NATBOX_HEALTH_URL:-http://127.0.0.1:8787/api/health}

if [ "$(id -u)" -ne 0 ]; then
  echo "run with sudo: curl -fsSL https://raw.githubusercontent.com/$repo/main/install-online.sh | sudo sh" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if command -v curl >/dev/null 2>&1; then
  download() { curl --fail --location --silent --show-error --retry 3 --output "$2" "$1"; }
  read_url() { curl --fail --location --silent --show-error --retry 3 "$1"; }
  health_check() { curl --fail --silent --max-time 3 "$1" >/dev/null 2>&1; }
elif command -v wget >/dev/null 2>&1; then
  download() { wget --quiet --tries=3 --output-document="$2" "$1"; }
  read_url() { wget --quiet --tries=3 --output-document=- "$1"; }
  health_check() { wget --quiet --timeout=3 --tries=1 --output-document=/dev/null "$1"; }
else
  echo "curl or wget is required" >&2
  exit 1
fi

if [ -n "$requested_version" ]; then
  version=$requested_version
else
  release_json=$(read_url "https://api.github.com/repos/$repo/releases/latest")
  version=$(printf '%s\n' "$release_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
  if [ -z "$version" ]; then
    echo "could not determine the latest Natbox release" >&2
    exit 1
  fi
fi
case "$version" in
  v*) : ;;
  *) version="v$version" ;;
esac

temporary_dir=$(mktemp -d)
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT
base_url="https://github.com/$repo/releases/download/$version"
download "$base_url/natbox-linux-$arch" "$temporary_dir/natbox"
download "$base_url/natbox-hash-linux-$arch" "$temporary_dir/natbox-hash"
download "$base_url/SHA256SUMS" "$temporary_dir/SHA256SUMS"
download "https://raw.githubusercontent.com/$repo/$version/natbox.service" "$temporary_dir/natbox.service"

if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum "$1" | awk '{print $1}'; }
else
  checksum() { shasum -a 256 "$1" | awk '{print $1}'; }
fi
for artifact in natbox natbox-hash; do
  if [ "$artifact" = natbox ]; then release_name="natbox-linux-$arch"; else release_name="natbox-hash-linux-$arch"; fi
  expected=$(awk -v file="$release_name" '$2 == file || $2 == "*" file {print $1; exit}' "$temporary_dir/SHA256SUMS")
  actual=$(checksum "$temporary_dir/$artifact")
  if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then
    echo "checksum verification failed for $release_name" >&2
    exit 1
  fi
done

mkdir -p "$install_dir" /etc/natbox "$state_dir" "$state_dir/upgrade-backups"
install_atomic() {
  source=$1
  target=$2
  temporary="$target.tmp.$$"
  install -m 0755 "$source" "$temporary"
  mv -f "$temporary" "$target"
}
install_atomic "$temporary_dir/natbox" "$install_dir/natbox"
install_atomic "$temporary_dir/natbox-hash" "$install_dir/natbox-hash"
install -m 0644 "$temporary_dir/natbox.service" "$service_file"

if [ ! -f /etc/natbox/natbox.env ]; then
  cat > /etc/natbox/natbox.env <<'EOF'
# Keep loopback unless you put Natbox behind an authenticated reverse proxy.
NATBOX_LISTEN=127.0.0.1:8787
NATBOX_DB=/var/lib/natbox/natbox.db
# Generate a hash with: /opt/natbox/natbox-hash
# NATBOX_ADMIN_PASSWORD_HASH=$argon2id$v=19$m=65536,t=3,p=2$...
# NATBOX_COOKIE_SECURE=1
# NATBOX_REQUIRE_TLS=1
# NATBOX_TOKEN=replace-with-a-long-random-token
# NATBOX_PORT_MIN=1
# NATBOX_PORT_MAX=65535
# NATBOX_SSH_PORT_MIN=2201
# NATBOX_SSH_PORT_MAX=2299
# NATBOX_TLS_CERT=/etc/natbox/tls/fullchain.pem
# NATBOX_TLS_KEY=/etc/natbox/tls/privkey.pem
# NATBOX_MEMORY_RESERVE_MB=256
# NATBOX_DISK_RESERVE_GB=1
# NATBOX_BACKUP_DIR=/var/lib/natbox/backups
# NATBOX_BACKUP_INTERVAL_MIN=360
# NATBOX_BACKUP_RETENTION=7
EOF
  chmod 0600 /etc/natbox/natbox.env
fi

systemctl daemon-reload
systemctl enable --now natbox.service
if ! systemctl is-active --quiet natbox.service; then
  systemctl --no-pager --full status natbox.service || true
  echo "natbox.service did not become active" >&2
  exit 1
fi
healthy=0
for _ in 1 2 3 4 5 6 7 8 9 10
do
  if health_check "$health_url"; then healthy=1; break; fi
  sleep 1
done
if [ "$healthy" -ne 1 ]; then
  echo "Natbox service started but health check failed: $health_url" >&2
  systemctl --no-pager --full status natbox.service || true
  exit 1
fi
echo "Natbox $version installed successfully ($arch)"
echo "Open through SSH: ssh -L 8787:127.0.0.1:8787 root@YOUR_VPS"

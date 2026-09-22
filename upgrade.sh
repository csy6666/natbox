#!/bin/sh
set -eu

# Upgrade a running Natbox installation from a GitHub Release. The current
# binary is backed up before replacement and restored if the service health
# check fails after restart.
repo=${NATBOX_REPO:-csy6666/natbox}
version=${NATBOX_VERSION:-latest}
install_dir=${NATBOX_INSTALL_DIR:-/opt/natbox}
binary="$install_dir/natbox"
hash_binary="$install_dir/natbox-hash"
health_url=${NATBOX_HEALTH_URL:-http://127.0.0.1:8787/api/health}
backup_dir=${NATBOX_UPGRADE_BACKUP_DIR:-/var/lib/natbox/upgrade-backups}
service_name=${NATBOX_SERVICE_NAME:-natbox.service}

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if command -v curl >/dev/null 2>&1; then
  download() { curl --fail --location --silent --show-error --retry 3 --output "$2" "$1"; }
  health_check() { curl --fail --silent --max-time 3 "$1" >/dev/null 2>&1; }
elif command -v wget >/dev/null 2>&1; then
  download() { wget --quiet --tries=3 --output-document="$2" "$1"; }
  health_check() { wget --quiet --timeout=3 --tries=1 --output-document=/dev/null "$1"; }
else
  echo "curl or wget is required" >&2
  exit 1
fi

case "$version" in
  latest) release_path=latest/download ;;
  v*) release_path=download/$version ;;
  *) release_path=download/v$version ;;
esac

temporary_dir=$(mktemp -d)
backup_path=""
hash_backup_path=""
cleanup() { rm -rf "$temporary_dir"; }
rollback() {
  if [ -n "$backup_path" ] && [ -f "$backup_path" ]; then
    install -m 0755 "$backup_path" "$binary"
    if [ -n "$hash_backup_path" ] && [ -f "$hash_backup_path" ]; then
      install -m 0755 "$hash_backup_path" "$hash_binary"
    fi
    systemctl restart "$service_name" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

mkdir -p "$install_dir" "$backup_dir"
download "https://github.com/$repo/releases/$release_path/natbox-linux-$arch" "$temporary_dir/natbox"
download "https://github.com/$repo/releases/$release_path/natbox-hash-linux-$arch" "$temporary_dir/natbox-hash"
download "https://github.com/$repo/releases/$release_path/SHA256SUMS" "$temporary_dir/SHA256SUMS"

expected=$(awk -v file="natbox-linux-$arch" '$2 == file || $2 == "*" file {print $1; exit}' "$temporary_dir/SHA256SUMS")
if [ -z "$expected" ]; then
  echo "release checksum for natbox-linux-$arch was not found" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$temporary_dir/natbox" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$temporary_dir/natbox" | awk '{print $1}')
fi
if [ "$actual" != "$expected" ]; then
  echo "checksum verification failed" >&2
  exit 1
fi

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
if [ -f "$binary" ]; then
  backup_path="$backup_dir/natbox-$timestamp"
  install -m 0755 "$binary" "$backup_path"
fi
if [ -f "$hash_binary" ]; then
  hash_backup_path="$backup_dir/natbox-hash-$timestamp"
  install -m 0755 "$hash_binary" "$hash_backup_path"
fi

systemctl stop "$service_name"
if ! install -m 0755 "$temporary_dir/natbox" "$binary.tmp.$$" || ! mv -f "$binary.tmp.$$" "$binary"; then
  rollback
  echo "could not replace Natbox binary; previous binary restored" >&2
  exit 1
fi
if ! install -m 0755 "$temporary_dir/natbox-hash" "$hash_binary.tmp.$$" || ! mv -f "$hash_binary.tmp.$$" "$hash_binary"; then
  rollback
  echo "could not replace natbox-hash; previous binaries restored" >&2
  exit 1
fi
if ! systemctl start "$service_name"; then
  rollback
  echo "service failed to start; previous binary restored" >&2
  exit 1
fi

healthy=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if health_check "$health_url"; then
    healthy=1
    break
  fi
  sleep 1
done
if [ "$healthy" -ne 1 ]; then
  echo "health check failed; restoring previous binary" >&2
  systemctl stop "$service_name" >/dev/null 2>&1 || true
  rollback
  exit 1
fi

echo "Natbox upgraded successfully ($arch, $version)"

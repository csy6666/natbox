#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
fake_bin="$work_dir/bin"
release_dir="$work_dir/release"
install_dir="$work_dir/install"
backup_dir="$work_dir/backups"
mkdir -p "$fake_bin" "$release_dir" "$install_dir" "$work_dir/tmp"

cat > "$fake_bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    --*) shift ;;
    *) url=$1; shift ;;
  esac
done
case "$url" in
  */api/health)
    [ "${NATBOX_TEST_SCENARIO:-}" != health-fail ]
    exit
    ;;
esac
case "$url" in
  */natbox-linux-amd64) source="$NATBOX_TEST_RELEASE/natbox-linux-amd64" ;;
  */natbox-hash-linux-amd64) source="$NATBOX_TEST_RELEASE/natbox-hash-linux-amd64" ;;
  */SHA256SUMS) source="$NATBOX_TEST_RELEASE/SHA256SUMS" ;;
  *) exit 2 ;;
esac
if [ "${NATBOX_TEST_SCENARIO:-}" = download-fail ] && [ "$source" != "$NATBOX_TEST_RELEASE/natbox-linux-amd64" ]; then
  exit 22
fi
cp "$source" "$output"
EOF
cat > "$fake_bin/systemctl" <<'EOF'
#!/bin/sh
set -eu
case "$1" in
  start) [ "${NATBOX_TEST_SCENARIO:-}" != start-fail ] ;;
esac
exit 0
EOF
real_mv=$(command -v mv)
cat > "$fake_bin/mv" <<'EOF'
#!/bin/sh
set -eu
case "${NATBOX_TEST_SCENARIO:-}:$*" in
  replace-fail:*natbox.tmp.*) exit 1 ;;
  hash-replace-fail:*natbox-hash.tmp.*) exit 1 ;;
esac
exec "$NATBOX_TEST_REAL_MV" "$@"
EOF
chmod +x "$fake_bin/id" "$fake_bin/curl" "$fake_bin/systemctl" "$fake_bin/mv"

printf 'new natbox\n' > "$release_dir/natbox-linux-amd64"
printf 'new hash\n' > "$release_dir/natbox-hash-linux-amd64"
natbox_sum=$(sha256sum "$release_dir/natbox-linux-amd64" | awk '{print $1}')
hash_sum=$(sha256sum "$release_dir/natbox-hash-linux-amd64" | awk '{print $1}')
printf '%s  natbox-linux-amd64\n%s  natbox-hash-linux-amd64\n' "$natbox_sum" "$hash_sum" > "$release_dir/SHA256SUMS"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}
assert_old_binaries() {
  printf 'old natbox\n' | cmp -s - "$install_dir/natbox" || fail "old natbox was not restored ($1)"
  printf 'old hash\n' | cmp -s - "$install_dir/natbox-hash" || fail "old natbox-hash was not restored ($1)"
}
run_upgrade() {
  PATH="$fake_bin:$PATH" \
  TMPDIR="$work_dir/tmp" \
  NATBOX_TEST_RELEASE="$release_dir" \
  NATBOX_TEST_REAL_MV="$real_mv" \
  NATBOX_TEST_SCENARIO="${1:-}" \
  NATBOX_INSTALL_DIR="$install_dir" \
  NATBOX_UPGRADE_BACKUP_DIR="$backup_dir" \
  NATBOX_VERSION=v-test \
  sh "$script_dir/upgrade.sh"
}
assert_cleanup() {
  [ -z "$(find "$work_dir/tmp" -mindepth 1 -print -quit)" ] || fail "temporary download directory was not removed ($1)"
  [ -z "$(find "$install_dir" -name '*.tmp.*' -print -quit)" ] || fail "replacement temporary file was not removed ($1)"
}

echo 'case: successful upgrade'
printf 'old natbox\n' > "$install_dir/natbox"
printf 'old hash\n' > "$install_dir/natbox-hash"
run_upgrade >/dev/null || fail 'successful upgrade returned an error'
printf 'new natbox\n' | cmp -s - "$install_dir/natbox" || fail 'Natbox binary was not upgraded'
printf 'new hash\n' | cmp -s - "$install_dir/natbox-hash" || fail 'natbox-hash binary was not upgraded'
assert_cleanup success

echo 'case: download failure'
printf 'old natbox\n' > "$install_dir/natbox"
printf 'old hash\n' > "$install_dir/natbox-hash"
if run_upgrade download-fail >/dev/null 2>&1; then fail 'download failure was accepted'; fi
assert_old_binaries download-fail
assert_cleanup download-fail

echo 'case: second binary checksum mismatch'
printf '%s  natbox-linux-amd64\n%064d  natbox-hash-linux-amd64\n' "$natbox_sum" 0 > "$release_dir/SHA256SUMS"
if run_upgrade >/dev/null 2>&1; then fail 'checksum mismatch was accepted'; fi
assert_old_binaries checksum-fail
assert_cleanup checksum-fail
printf '%s  natbox-linux-amd64\n%s  natbox-hash-linux-amd64\n' "$natbox_sum" "$hash_sum" > "$release_dir/SHA256SUMS"

echo 'case: replacement failure'
if run_upgrade replace-fail >/dev/null 2>&1; then fail 'replacement failure was accepted'; fi
assert_old_binaries replace-fail
assert_cleanup replace-fail

echo 'case: hash binary replacement failure'
if run_upgrade hash-replace-fail >/dev/null 2>&1; then fail 'hash binary replacement failure was accepted'; fi
assert_old_binaries hash-replace-fail
assert_cleanup hash-replace-fail

echo 'case: service start failure'
if run_upgrade start-fail >/dev/null 2>&1; then fail 'service start failure was accepted'; fi
assert_old_binaries start-fail
assert_cleanup start-fail

echo 'case: health check failure'
if run_upgrade health-fail >/dev/null 2>&1; then fail 'health failure was accepted'; fi
assert_old_binaries health-fail
assert_cleanup health-fail

echo 'all upgrade script tests passed'

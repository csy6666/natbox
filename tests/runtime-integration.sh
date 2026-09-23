#!/bin/sh
set -eu

fail() {
  echo "runtime integration: $*" >&2
  exit 1
}

: "${NATBOX_RUN_RUNTIME_INTEGRATION:?Set NATBOX_RUN_RUNTIME_INTEGRATION=1 to opt in}"
[ "$NATBOX_RUN_RUNTIME_INTEGRATION" = 1 ] || fail "opt-in value must be 1"
: "${NATBOX_TEST_PROJECT:?Set NATBOX_TEST_PROJECT to a dedicated disposable project}"
: "${NATBOX_TEST_STORAGE_POOL:?Set NATBOX_TEST_STORAGE_POOL to the project's dedicated storage pool}"
: "${NATBOX_TEST_NETWORK:?Set NATBOX_TEST_NETWORK to the project's dedicated NAT bridge}"
[ "${NATBOX_TEST_PROJECT#natbox-it-}" != "$NATBOX_TEST_PROJECT" ] || fail "project name must start with natbox-it-"
[ "$NATBOX_TEST_PROJECT" != default ] || fail "the default project is forbidden"

runtime=${NATBOX_RUNTIME:-}
if [ -z "$runtime" ]; then
  if command -v incus >/dev/null 2>&1; then runtime=incus
  elif command -v lxc >/dev/null 2>&1; then runtime=lxc
  else fail "Incus or LXD client is required"
  fi
fi
runtime=$(command -v "$runtime" 2>/dev/null || true)
[ -n "$runtime" ] || fail "NATBOX_RUNTIME must resolve to an executable"
for command in curl jq python3 ssh-keygen ssh go; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"

done

work_dir=$(mktemp -d)
port=${NATBOX_TEST_LISTEN_PORT:-18787}
token=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
project_wrapper="$work_dir/runtime-project"
server_pid=""
container_name=""
cleanup() {
  status=$?
  if [ -n "$container_name" ]; then
    "$project_wrapper" delete "$container_name" --force >/dev/null 2>&1 || true
  fi
  if [ -n "$server_pid" ]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$work_dir"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

cat > "$project_wrapper" <<'EOF'
#!/bin/sh
exec "$NATBOX_TEST_RUNTIME" --project "$NATBOX_TEST_PROJECT" "$@"
EOF
chmod 700 "$project_wrapper"
export NATBOX_TEST_RUNTIME="$runtime" NATBOX_TEST_PROJECT

project_json=$("$runtime" query "/1.0/projects/$NATBOX_TEST_PROJECT") || fail "cannot read the selected project"
printf '%s' "$project_json" | jq -e '.config["user.natbox.integration"] == "1"' >/dev/null || fail "project is not marked user.natbox.integration=1"
profile_json=$("$runtime" query "/1.0/profiles/default?project=$NATBOX_TEST_PROJECT") || fail "cannot read project default profile"
printf '%s' "$profile_json" | jq -e --arg pool "$NATBOX_TEST_STORAGE_POOL" --arg network "$NATBOX_TEST_NETWORK" '.devices.root.pool == $pool and (.devices.eth0.network == $network or .devices.eth0.parent == $network)' >/dev/null || fail "default profile must use only the declared test storage pool and network"
existing=$("$project_wrapper" list --format=json) || fail "cannot list project instances"
printf '%s' "$existing" | jq -e 'length == 0' >/dev/null || fail "the selected project must be empty"

version=$("$project_wrapper" version 2>&1 | tr '\n' ' ')
echo "runtime: $version"
echo "project: $NATBOX_TEST_PROJECT (empty and marked disposable)"

export GOWORK=off
go build -o "$work_dir/natbox" .
go build -o "$work_dir/natbox-hash" ./cmd/natbox-hash
NATBOX_RUNTIME="$project_wrapper" NATBOX_DB="$work_dir/natbox.db" NATBOX_LISTEN="127.0.0.1:$port" NATBOX_TOKEN="$token" "$work_dir/natbox" >"$work_dir/natbox.log" 2>&1 &
server_pid=$!
base_url="http://127.0.0.1:$port"
ready=0
for _ in $(seq 1 60); do
  if curl -fsS "$base_url/api/health" >/dev/null 2>&1; then ready=1; break; fi
  kill -0 "$server_pid" 2>/dev/null || { cat "$work_dir/natbox.log" >&2; fail "Natbox exited before becoming healthy"; }
  sleep 1
done
[ "$ready" = 1 ] || { cat "$work_dir/natbox.log" >&2; fail "Natbox health check timed out"; }
api() { curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' "$@"; }

container_name="natbox-it-$(date -u +%H%M%S)-$$"
image=${NATBOX_TEST_IMAGE:-images:alpine/3.24}
api -X POST "$base_url/api/v1/containers" -d "{\"name\":\"$container_name\",\"image\":\"$image\",\"memoryLimitMb\":120,\"diskLimitGb\":1,\"cpuLimit\":\"12%\"}" | jq -e --arg name "$container_name" '.success and .data.name == $name' >/dev/null || fail "container create failed"
instance=$("$project_wrapper" list --format=json | jq -ce --arg name "$container_name" '.[] | select(.name == $name)') || fail "created instance is missing"
ipv4=$(printf '%s' "$instance" | jq -r '[.state.network[]?.addresses[]? | select(.family == "inet" and .scope != "link" and .address != "127.0.0.1") | .address][0] // empty')
[ -n "$ipv4" ] || fail "container has no usable IPv4 address"
echo "container: $container_name, IPv4: $ipv4"

ssh-keygen -q -t ed25519 -N '' -f "$work_dir/id_ed25519"
public_key=$(cat "$work_dir/id_ed25519.pub")
api -X POST "$base_url/api/v1/containers/$container_name/ssh-key" -d "{\"publicKey\":\"$public_key\"}" | jq -e '.success' >/dev/null || fail "SSH provisioning failed"
ssh_forward=$(api -X POST "$base_url/api/v1/containers/$container_name/ssh" -d '{}' | jq -ce '.data') || fail "SSH forward allocation failed"
ssh_port=$(printf '%s' "$ssh_forward" | jq -r '.listenPort')
ssh_ready=0
for _ in $(seq 1 30); do
  if ssh -n -i "$work_dir/id_ed25519" -p "$ssh_port" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=2 root@127.0.0.1 true >/dev/null 2>&1; then ssh_ready=1; break; fi
  sleep 1
done
[ "$ssh_ready" = 1 ] || fail "SSH public-key login through the allocated forward failed"
echo "SSH: public-key login passed on TCP/$ssh_port"

tcp_port=${NATBOX_TEST_TCP_PORT:-18080}
udp_port=${NATBOX_TEST_UDP_PORT:-18081}
api -X POST "$base_url/api/v1/containers/$container_name/ports" -d "{\"protocol\":\"tcp\",\"listenPort\":$tcp_port,\"targetPort\":$tcp_port}" | jq -e '.success' >/dev/null || fail "TCP forward configuration failed"
api -X POST "$base_url/api/v1/containers/$container_name/ports" -d "{\"protocol\":\"udp\",\"listenPort\":$udp_port,\"targetPort\":$udp_port}" | jq -e '.success' >/dev/null || fail "UDP forward configuration failed"
"$project_wrapper" exec "$container_name" -- apk add --no-cache socat || fail "could not install in-container echo services"
"$project_wrapper" exec "$container_name" -- sh -c "printf '%s\\n' '#!/bin/sh' 'nohup socat TCP-LISTEN:$tcp_port,reuseaddr,fork SYSTEM:\"printf tcp-ok\" >/dev/null 2>&1 </dev/null &' 'nohup socat -T2 UDP-RECVFROM:$udp_port,reuseaddr,fork SYSTEM:\"printf udp-ok\" >/dev/null 2>&1 </dev/null &' > /etc/local.d/natbox-it.start && chmod 755 /etc/local.d/natbox-it.start && rc-update add local default && rc-service local start" || fail "could not configure/start in-container TCP/UDP echo services"
sleep 2
check_forwarding() {
  python3 - "$tcp_port" "$udp_port" <<'PY'
import socket
import sys

for port, kind, expected in ((int(sys.argv[1]), socket.SOCK_STREAM, b"tcp-ok"), (int(sys.argv[2]), socket.SOCK_DGRAM, b"udp-ok")):
    with socket.socket(socket.AF_INET, kind) as client:
        client.settimeout(5)
        client.connect(("127.0.0.1", port))
        client.sendall(b"natbox-probe") if kind == socket.SOCK_STREAM else client.send(b"natbox-probe")
        actual = client.recv(128)
        if expected not in actual:
            raise SystemExit(f"port {port}: expected {expected!r}, received {actual!r}")
PY
}
check_forwarding || fail "TCP/UDP data forwarding failed"
echo 'TCP and UDP data forwarding: passed'

api -X POST "$base_url/api/v1/containers/$container_name/restart" -d '{}' | jq -e '.success' >/dev/null || fail "container restart failed"
check_forwarding || fail "TCP/UDP forwarding failed after restart"
echo 'TCP and UDP forwarding after restart: passed'
api -X POST "$base_url/api/v1/containers/$container_name/stop" -d '{}' | jq -e '.success' >/dev/null || fail "container stop failed"
api -X POST "$base_url/api/v1/containers/$container_name/start" -d '{}' | jq -e '.success' >/dev/null || fail "container start failed"
check_forwarding || fail "TCP/UDP forwarding failed after stop/start"
echo 'stop/start lifecycle: passed'

failed_name="$container_name-fail"
if api -X POST "$base_url/api/v1/containers" -d "{\"name\":\"$failed_name\",\"image\":\"$image\",\"memoryLimitMb\":120,\"diskLimitGb\":1,\"publicKey\":\"invalid\"}" >/dev/null 2>&1; then
  fail "invalid SSH key unexpectedly allowed create to succeed"
fi
if "$project_wrapper" list --format=json | jq -e --arg name "$failed_name" 'any(.[]; .name == $name)' >/dev/null; then
  fail "failed create left a runtime instance behind"
fi
echo 'failed create rollback: passed'

rx=0
tx=0
for _ in $(seq 1 30); do
  stats=$(api "$base_url/api/v1/containers" | jq -ce --arg name "$container_name" '.data[] | select(.name == $name) | .traffic') || break
  rx=$(printf '%s' "$stats" | jq -r '.rxBytes // 0')
  tx=$(printf '%s' "$stats" | jq -r '.txBytes // 0')
  [ "$rx" -gt 0 ] && [ "$tx" -gt 0 ] && break
  sleep 1
done
[ "$rx" -gt 0 ] && [ "$tx" -gt 0 ] || fail "runtime receive/send counters did not increase"
echo "runtime counters: rx=$rx tx=$tx"

api -X POST "$base_url/api/v1/containers/$container_name/delete" -d '{}' | jq -e '.success' >/dev/null || fail "container delete failed"
container_name=""
remaining=$("$project_wrapper" list --format=json | jq -r 'length')
[ "$remaining" = 0 ] || fail "test project contains leftover instances"
echo 'delete and project cleanup: passed'

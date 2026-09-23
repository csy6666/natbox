# Incus/LXD Runtime Integration Test

This opt-in test creates and deletes one Alpine instance through the Natbox HTTP API. It exercises IPv4 acquisition, SSH key setup and login, real TCP and UDP data forwarding, forwarding after restart and stop/start, failed-create rollback, runtime counters, and deletion.

It does not create or delete the Incus/LXD project, network, storage pool, profiles, images, or host firewall rules. Prepare a disposable project and its dedicated NAT bridge and storage pool before running it. The script refuses the default project, requires a `natbox-it-` project name, checks a project marker, verifies the default profile points at the declared pool and bridge, and requires that the project has no instances.

## Isolated Project Preparation

Run these commands on a disposable Linux test host with Incus or LXD initialized. Pick a fresh project, pool, and bridge name. Do not use a production project or a profile shared with other projects.

For Incus, create a project and enable its resource namespaces:

```sh
PROJECT=natbox-it-manual
POOL=natbox-it-pool
NETWORK=natbox-it-br0
incus project create "$PROJECT" -c features.images=true -c features.networks=true -c features.profiles=true -c features.storage.volumes=true -c user.natbox.integration=1
incus project switch "$PROJECT"
incus storage create "$POOL" dir
incus network create "$NETWORK" --type=bridge ipv4.address=10.239.240.1/24 ipv4.nat=true ipv6.address=none
incus profile device add default root disk path=/ pool="$POOL"
incus profile device add default eth0 nic name=eth0 network="$NETWORK"
incus project switch default
```

For LXD, run the equivalent commands using `lxc`; confirm your installed LXD version supports the project features above before creating resources:

```sh
PROJECT=natbox-it-manual
POOL=natbox-it-pool
NETWORK=natbox-it-br0
lxc project create "$PROJECT" -c features.images=true -c features.networks=true -c features.profiles=true -c features.storage.volumes=true -c user.natbox.integration=1
lxc project switch "$PROJECT"
lxc storage create "$POOL" dir
lxc network create "$NETWORK" --type=bridge ipv4.address=10.239.240.1/24 ipv4.nat=true ipv6.address=none
lxc profile device add default root disk path=/ pool="$POOL"
lxc profile device add default eth0 nic name=eth0 network="$NETWORK"
lxc project switch default
```

Some Incus 6 installations do not allow a traditional `bridge` network to be project-scoped. In that case create the marked project first, create the dedicated bridge in the default project, and attach it to the test project's profile as a bridged parent:

```sh
incus network create "$NETWORK" --type=bridge ipv4.address=10.239.240.1/24 ipv4.nat=true ipv6.address=none
incus --project "$PROJECT" profile device add default eth0 nic name=eth0 nictype=bridged parent="$NETWORK"
```

The test accepts either `devices.eth0.network` or `devices.eth0.parent` when checking the declared bridge. The bridge remains inside the disposable Incus host/container.

The test image must be available from the selected project's image remote. The container needs outbound package access to install OpenSSH and socat. Make sure host TCP ports 18080, 2201-2299 and UDP port 18081 are available; override the test forwarding ports with `NATBOX_TEST_TCP_PORT` and `NATBOX_TEST_UDP_PORT` when needed. Do not expose the Natbox test listener beyond loopback.

## Run

Build dependencies are Go, curl, jq, Python 3, OpenSSH client/keygen, and the Incus or LXD CLI. Run from the Natbox repository root:

```sh
export NATBOX_RUN_RUNTIME_INTEGRATION=1
export NATBOX_RUNTIME=incus
export NATBOX_TEST_PROJECT=natbox-it-manual
export NATBOX_TEST_STORAGE_POOL=natbox-it-pool
export NATBOX_TEST_NETWORK=natbox-it-br0
sh tests/runtime-integration.sh
```

Use `NATBOX_RUNTIME=lxc` for LXD, or omit it to select Incus first and LXD second. The script prints the runtime version and selected project before creating an instance. A successful run ends with `delete and project cleanup: passed`, and verifies the selected project is empty again. It only deletes the uniquely named test instance it created. The runtime project, profile, network, and pool remain for manual review and teardown.

To run against a different Alpine image, set `NATBOX_TEST_IMAGE`. The script uses TCP/18080 and UDP/18081 by default. It polls a loopback Natbox listener on TCP/18787 by default; set `NATBOX_TEST_LISTEN_PORT` if that port is occupied.

## Failure and Teardown

If a run is interrupted, inspect the selected project before removing anything:

```sh
incus --project natbox-it-manual list
incus --project natbox-it-manual info <reported-test-instance>
```

Remove only the instance whose name starts with `natbox-it-` and matches the failed run. After confirming the project is empty and no other workload uses its resources, tear down the project resources in dependency order:

```sh
incus project switch natbox-it-manual
incus profile device remove default eth0
incus profile device remove default root
incus network delete natbox-it-br0
incus project switch default
incus project delete natbox-it-manual
incus storage delete natbox-it-pool
```

Repeat with `lxc` for LXD. Delete the project-scoped network before switching away; storage pools are host-wide, so verify no other project references the pool before deletion. Record the exact client/server version printed by the test and any LXD/Incus differences in the release acceptance notes. A successful script run is evidence for that one host, version, image, bridge, and storage driver only; it does not establish a compatibility matrix.

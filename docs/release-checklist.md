# Natbox Release Checklist

Use this checklist for a release candidate. Keep the release tree free of credentials, SQLite databases, backups, generated client links, and real host addresses.

## Automated Gates

- [ ] `GOWORK=off go test ./... -count=1`
- [ ] `GOWORK=off go test -race ./... -count=1`
- [ ] `GOWORK=off go vet ./...`
- [ ] `gofmt -l` returns no changed Go files
- [ ] `git diff --check` passes
- [ ] `sh -n upgrade.sh install.sh install-online.sh tests/*.sh`
- [ ] `sh tests/upgrade_test.sh` passes successful upgrade, both checksum failures, replacement failures, service-start failure, health failure, rollback, and temporary-file cleanup
- [ ] Linux amd64 and arm64 builds complete with `CGO_ENABLED=0`
- [ ] Release `SHA256SUMS` contains both `natbox-*` and `natbox-hash-*` artifacts for each architecture
- [ ] A clean checkout reproduces the build without parent `go.work` dependencies

## API and Compatibility

- [ ] `openapi.yaml` and the versioned `/api/v1` routes match the implementation
- [ ] Legacy `/api/...` compatibility routes still pass focused HTTP tests
- [ ] Error responses retain stable `code` values and do not include credentials or private keys
- [ ] Upgrade preserves the SQLite database and environment file

## Isolated Runtime Acceptance

- [ ] Run [`runtime-integration.md`](runtime-integration.md) on a disposable Linux host with Incus
- [ ] Repeat on a separate disposable LXD host, recording client/server versions and differences
- [ ] The project is empty, named with the `natbox-it-` prefix, and marked `user.natbox.integration=1`
- [ ] Create and IPv4 acquisition pass
- [ ] SSH key provisioning and login through an allocated TCP forward pass
- [ ] Real TCP and UDP forwarding pass before and after restart and stop/start
- [ ] Failed creation leaves no runtime instance behind
- [ ] Runtime receive/send counters increase after known traffic
- [ ] Delete leaves the test project empty

The runtime script is opt-in and is not part of default CI because it requires privileged Incus/LXD networking and a disposable host.

## Policy Acceptance

- [ ] A quota at or above the sampled receive/send delta stops the container and writes a `policy.stop` audit event
- [ ] An expired container stops without requiring a stats read
- [ ] A temporary stats failure leaves the container running for that pass and logs the failure
- [ ] Document the one-minute sampling interval as a soft limit; do not claim an exact hard cap
- [ ] For a test quota, record observed overage between traffic generation and the next enforcement sample

## Release Evidence

- [ ] Record exact Natbox version, Go version, Linux distribution, runtime name/version, image, network, and storage driver
- [ ] Attach test logs without tokens, keys, passwords, database files, or public production addresses
- [ ] Record administrator rollback command and previous artifact checksum
- [ ] Do not publish, tag, push, or upgrade a production host without explicit authorization

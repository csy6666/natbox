package incus

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type versionFallbackRunner struct{}

func (versionFallbackRunner) Run(_ context.Context, args ...string) (string, string, error) {
	if len(args) == 2 && args[0] == "version" && args[1] == "--format=json" {
		return "", "unknown flag", errors.New("unknown flag")
	}
	if len(args) == 1 && args[0] == "version" {
		return "Client version: 5.21.7 LTS\n", "", nil
	}
	if len(args) == 3 && args[0] == "storage" {
		return `[{"driver":"dir"}]`, "", nil
	}
	if len(args) == 3 && args[0] == "network" {
		return `[{"name":"lxdbr0"}]`, "", nil
	}
	return "", "", nil
}

type fakeRunner struct {
	calls   [][]string
	outputs map[string]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := ""
	for _, arg := range args {
		if key != "" {
			key += " "
		}
		key += arg
	}
	return f.outputs[key], "", nil
}

func TestCreateUsesStructuredArguments(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{}}
	c, err := New(f)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Create(context.Background(), InstanceSpec{Name: "nat01", Image: "images:alpine/3.20", MemoryBytes: 120 * 1024 * 1024, RootDiskBytes: 1024 * 1024 * 1024, CPUAllowance: "12%"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"init", "images:alpine/3.20", "nat01", "--config", "limits.memory=120MiB", "--config", "limits.cpu.allowance=12%"},
		{"config", "device", "override", "nat01", "root", "size=1024MiB"},
		{"config", "set", "nat01", "boot.autostart", "true"},
		{"start", "nat01"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %#v, want %#v", f.calls, want)
	}
}

type failingCreateRunner struct {
	calls [][]string
}

func (f *failingCreateRunner) Run(_ context.Context, args ...string) (string, string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) >= 3 && args[0] == "config" && args[1] == "device" {
		return "", "disk failed", errors.New("disk failed")
	}
	return "", "", nil
}

func TestCreateRollsBackAfterConfigurationFailure(t *testing.T) {
	f := &failingCreateRunner{}
	c, _ := New(f)
	err := c.Create(context.Background(), InstanceSpec{Name: "nat01", Image: "images:alpine/3.20", MemoryBytes: 120 * 1024 * 1024, RootDiskBytes: 1024 * 1024 * 1024})
	if err == nil {
		t.Fatal("expected configuration failure")
	}
	if len(f.calls) != 3 || !reflect.DeepEqual(f.calls[2], []string{"delete", "nat01", "--force"}) {
		t.Fatalf("rollback calls = %#v", f.calls)
	}
}

func TestWaitIPv4(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"list --format=json": `[{"name":"nat01","state":{"network":{"eth0":{"addresses":[{"family":"inet","address":"10.88.0.12","scope":"global"}]}}}}]`,
	}}
	c, _ := New(f)
	address, err := c.WaitIPv4(context.Background(), "nat01")
	if err != nil || address != "10.88.0.12" {
		t.Fatalf("address=%q err=%v", address, err)
	}
}

func TestIPv4SkipsLoopback(t *testing.T) {
	instance := Instance{State: InstanceState{Network: map[string]NetworkState{
		"lo":   {Addresses: []NetworkAddress{{Family: "inet", Address: "127.0.0.1", Scope: "host"}}},
		"eth0": {Addresses: []NetworkAddress{{Family: "inet", Address: "10.88.0.12", Scope: "global"}}},
	}}}
	if got := instance.IPv4(); got != "10.88.0.12" {
		t.Fatalf("IPv4 = %q", got)
	}
}

func TestProbeFallsBackToLXDHumanVersion(t *testing.T) {
	c, _ := New(versionFallbackRunner{})
	capabilities, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.Installed || capabilities.Version != "5.21.7 LTS" || len(capabilities.StorageDrivers) != 1 || len(capabilities.Networks) != 1 {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestListPortForwards(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"config device show nat01 --format=json": `{"eth0":{"type":"nic"},"proxy-tcp-2201":{"type":"proxy","listen":"tcp:0.0.0.0:2201","connect":"tcp:127.0.0.1:22"},"proxy-udp-5301":{"type":"proxy","listen":"udp:0.0.0.0:5301","connect":"udp:127.0.0.1:53"}}`,
	}}
	c, _ := New(f)
	got, err := c.ListPortForwards(context.Background(), "nat01")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ListenPort != 2201 || got[1].Protocol != "udp" {
		t.Fatalf("unexpected forwards: %#v", got)
	}
}

func TestUnsafePortForwardNameDoesNotRun(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{}}
	c, _ := New(f)
	if err := c.AddPortForward(context.Background(), "nat;rm", "tcp", 2201, 22); err == nil {
		t.Fatal("expected invalid name")
	}
	if len(f.calls) != 0 {
		t.Fatalf("runner calls = %#v", f.calls)
	}
}

func TestAddPortForwardRejectsDuplicateListenPort(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"config device show nat01 --format=json": `{"proxy-tcp-2201":{"type":"proxy","listen":"tcp:0.0.0.0:2201","connect":"tcp:127.0.0.1:22"}}`,
	}}
	c, _ := New(f)
	if err := c.AddPortForward(context.Background(), "nat01", "tcp", 2201, 2222); err == nil {
		t.Fatal("expected duplicate listen port error")
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected only device inspection, got %#v", f.calls)
	}
}

func TestAddPortForwardUsesContainerIPv4WithoutNatMode(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"config device show nat01 --format=json": `{}`,
		"list --format=json":                     `[{"name":"nat01","state":{"network":{"eth0":{"addresses":[{"family":"inet","address":"10.88.0.12","scope":"global"}]}}}}]`,
	}}
	c, _ := New(f)
	if err := c.AddPortForward(context.Background(), "nat01", "tcp", 2201, 22); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1]; !reflect.DeepEqual(got, []string{"config", "device", "add", "nat01", "proxy-tcp-2201", "proxy", "listen=tcp:0.0.0.0:2201", "connect=tcp:10.88.0.12:22"}) {
		t.Fatalf("unexpected add args: %#v", got)
	}
}

func TestInfoParsesResourceUsage(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"info nat01 --format=json": `{"status":"Running","status_code":103,"started_at":"2026-09-21T00:00:00Z","memory":{"usage":12000000,"total":125829120},"disk":{"root":{"usage":1048576,"total":1073741824}}}`,
	}}
	c, _ := New(f)
	info, err := c.Info(context.Background(), "nat01")
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "Running" || info.Memory.Usage != 12000000 || info.Disk["root"].Total != 1073741824 {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestStatsReadsNetworkCounters(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{
		"query /1.0/instances/nat01/state": `{"network":{"eth0":{"counters":{"bytes_received":10,"bytes_sent":20,"packets_received":2,"packets_sent":3}},"lo":{"counters":{"bytes_received":100,"bytes_sent":100}}}}`,
	}}
	c, _ := New(f)
	stats, err := c.Stats(context.Background(), "nat01")
	if err != nil {
		t.Fatal(err)
	}
	if stats.RxBytes != 10 || stats.TxBytes != 20 || stats.RxPackets != 2 || stats.TxPackets != 3 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestDeleteUsesForce(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{}}
	c, _ := New(f)
	if err := c.Delete(context.Background(), "nat01"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], []string{"delete", "nat01", "--force"}) {
		t.Fatalf("unexpected delete args: %#v", f.calls)
	}
}

func TestConfigureSSHUsesEncodedKeyArgument(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{}}
	c, _ := New(f)
	key := "ssh-ed25519 AAAA comment"
	if err := c.ConfigureSSH(context.Background(), "nat01", key); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0][0] != "exec" {
		t.Fatalf("unexpected calls: %#v", f.calls)
	}
	args := f.calls[0]
	if args[len(args)-1] == key || args[len(args)-1] == "" {
		t.Fatalf("public key was not encoded: %#v", args)
	}
}

func TestConfigureSSHRejectsUnsupportedKey(t *testing.T) {
	f := &fakeRunner{outputs: map[string]string{}}
	c, _ := New(f)
	if err := c.ConfigureSSH(context.Background(), "nat01", "ssh-dss AAAA"); err == nil {
		t.Fatal("expected unsupported key error")
	}
	if len(f.calls) != 0 {
		t.Fatalf("runner calls = %#v", f.calls)
	}
}

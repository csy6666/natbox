package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"natbox/internal/incus"
	"natbox/internal/store"
)

type policyRunner struct {
	outputs map[string]string
	errors  map[string]error
	calls   [][]string
}

func (r *policyRunner) Run(_ context.Context, args ...string) (string, string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	key := strings.Join(args, " ")
	if err := r.errors[key]; err != nil {
		return "", "stats unavailable", err
	}
	return r.outputs[key], "", nil
}

func newPolicyTestApp(t *testing.T, runner *policyRunner) *app {
	t.Helper()
	database, err := store.Open(t.TempDir() + "\\natbox.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	client, err := incus.New(runner)
	if err != nil {
		t.Fatal(err)
	}
	return &app{incus: client, store: database, startedAt: time.Now().UTC()}
}

func addPolicyContainer(t *testing.T, a *app, item store.ManagedContainer) {
	t.Helper()
	if err := a.store.UpsertContainer(context.Background(), item); err != nil {
		t.Fatal(err)
	}
}

func policyCall(calls [][]string, want ...string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func TestCounterDeltaTreatsRuntimeCounterResetAsZero(t *testing.T) {
	if got := counterDelta(50, 100); got != 0 {
		t.Fatalf("counterDelta after reset = %d, want 0", got)
	}
	if got := counterDelta(150, 100); got != 50 {
		t.Fatalf("counterDelta = %d, want 50", got)
	}
}

func TestEnforcePolicyPassStopsAtQuotaAndAudits(t *testing.T) {
	runner := &policyRunner{outputs: map[string]string{
		"list --format=json":               `[{"name":"nat01","status":"Running","type":"container"}]`,
		"query /1.0/instances/nat01/state": `{"network":{"eth0":{"counters":{"bytes_received":70,"bytes_sent":60}}}}`,
	}}
	a := newPolicyTestApp(t, runner)
	addPolicyContainer(t, a, store.ManagedContainer{
		Name: "nat01", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1,
		DesiredState: "running", QuotaBytes: 100, QuotaRxBaseline: 10, QuotaTxBaseline: 20,
	})

	a.enforcePolicyPass(context.Background())
	if !policyCall(runner.calls, "stop", "nat01") {
		t.Fatalf("runtime calls = %#v, want stop nat01", runner.calls)
	}
	items, err := a.store.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DesiredState != "stopped" {
		t.Fatalf("managed state = %#v, want stopped", items)
	}
	events, err := a.store.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "policy.stop" || events[0].Result != "success" || !strings.Contains(events[0].Details, "quota reached") {
		t.Fatalf("audit events = %#v, want successful quota stop", events)
	}
}

func TestEnforcePolicyPassStopsExpiredContainerWithoutStats(t *testing.T) {
	runner := &policyRunner{outputs: map[string]string{
		"list --format=json": `[{"name":"nat01","status":"Running","type":"container"}]`,
	}}
	a := newPolicyTestApp(t, runner)
	addPolicyContainer(t, a, store.ManagedContainer{
		Name: "nat01", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1,
		DesiredState: "running", ExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})

	a.enforcePolicyPass(context.Background())
	if !policyCall(runner.calls, "stop", "nat01") {
		t.Fatalf("runtime calls = %#v, want stop nat01", runner.calls)
	}
	if policyCall(runner.calls, "query", "/1.0/instances/nat01/state") {
		t.Fatalf("runtime calls = %#v, expiry should not require stats", runner.calls)
	}
	events, err := a.store.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !strings.Contains(events[0].Details, "expired") {
		t.Fatalf("audit events = %#v, want expiry audit", events)
	}
}

func TestEnforcePolicyPassFailsOpenWhenStatsUnavailable(t *testing.T) {
	runner := &policyRunner{
		outputs: map[string]string{
			"list --format=json": `[{"name":"nat01","status":"Running","type":"container"}]`,
		},
		errors: map[string]error{
			"query /1.0/instances/nat01/state": errors.New("temporary stats failure"),
		},
	}
	a := newPolicyTestApp(t, runner)
	addPolicyContainer(t, a, store.ManagedContainer{
		Name: "nat01", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1,
		DesiredState: "running", QuotaBytes: 100, QuotaRxBaseline: 10, QuotaTxBaseline: 20,
	})

	a.enforcePolicyPass(context.Background())
	if policyCall(runner.calls, "stop", "nat01") {
		t.Fatalf("runtime calls = %#v, stats failure must not stop the container", runner.calls)
	}
	items, err := a.store.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DesiredState != "running" {
		t.Fatalf("managed state = %#v, want running", items)
	}
	events, err := a.store.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("audit events = %#v, stats failure should not create policy.stop audit", events)
	}
}

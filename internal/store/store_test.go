package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStorePersistsManagedContainersAndAudit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "natbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.UpsertContainer(ctx, ManagedContainer{Name: "nat01", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1, CPULimit: "12%", DesiredState: "running"}); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListContainers(ctx)
	if err != nil || len(items) != 1 || items[0].Name != "nat01" {
		t.Fatalf("managed containers = %#v, err=%v", items, err)
	}
	if err := db.Audit(ctx, AuditEvent{Actor: "test", Action: "container.create", Target: "container", TargetID: "nat01", Details: "safe"}); err != nil {
		t.Fatal(err)
	}
	events, err := db.ListAudit(ctx, 10, 0)
	if err != nil || len(events) != 1 || events[0].TargetID != "nat01" {
		t.Fatalf("audit events = %#v, err=%v", events, err)
	}
	if err := db.SetDesiredState(ctx, "nat01", "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteContainer(ctx, "nat01"); err != nil {
		t.Fatal(err)
	}
}

func TestParseBeforeID(t *testing.T) {
	if ParseBeforeID("abc") != 0 || ParseBeforeID("0") != 0 || ParseBeforeID("12") != 12 {
		t.Fatal("unexpected before id parsing")
	}
}

func TestBackupRestoreReplacesManagedDeclarations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "natbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.UpsertContainer(ctx, ManagedContainer{Name: "old", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1, DesiredState: "running"}); err != nil {
		t.Fatal(err)
	}
	backup := Backup{Version: 1, Containers: []ManagedContainer{{Name: "new", Image: "images:alpine/3.24", MemoryLimitMb: 256, DiskLimitGb: 2, DesiredState: "stopped"}}}
	if err := db.Restore(ctx, backup); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListContainers(ctx)
	if err != nil || len(items) != 1 || items[0].Name != "new" || items[0].DesiredState != "stopped" {
		t.Fatalf("restored containers = %#v, err=%v", items, err)
	}
}

func TestPolicyRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "natbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.UpsertContainer(ctx, ManagedContainer{Name: "nat01", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1, DesiredState: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdatePolicy(ctx, "nat01", 1000, 10, 20, "2026-09-22T00:00:00Z", "2026-09-23T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListContainers(ctx)
	if err != nil || len(items) != 1 || items[0].QuotaBytes != 1000 || items[0].QuotaRxBaseline != 10 || items[0].ExpiresAt == "" {
		t.Fatalf("policy = %#v, err=%v", items, err)
	}
}

func TestBackupDatabasePublishesPrivateSQLiteCopy(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "natbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	destination := filepath.Join(t.TempDir(), "backups", "natbox.db")
	if err := db.BackupDatabase(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("backup stat = %#v, err=%v", info, err)
	}
}

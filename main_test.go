package main

import (
	"strings"
	"testing"
)

func TestValidateRange(t *testing.T) {
	if err := validateRange(2201, 2299, 1, 65535); err != nil {
		t.Fatal(err)
	}
	if err := validateRange(0, 2299, 1, 65535); err == nil {
		t.Fatal("expected lower bound error")
	}
}

func TestFormatBytes(t *testing.T) {
	if got := formatBytes(120 * 1024 * 1024); got != "120.0 MiB" {
		t.Fatalf("formatBytes = %q", got)
	}
	if got := formatBytes(5 * 1024 * 1024 * 1024); got != "5.0 GiB" {
		t.Fatalf("formatBytes = %q", got)
	}
}

func TestBulkNames(t *testing.T) {
	names, err := bulkNames("nat", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nat01", "nat02", "nat03"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %#v, want %#v", names, want)
		}
	}
}

func TestBulkNamesRejectsLongGeneratedName(t *testing.T) {
	if _, err := bulkNames(strings.Repeat("a", 59), 999999, 1); err == nil {
		t.Fatal("expected generated name length error")
	}
}

func TestValidateCreateRequest(t *testing.T) {
	valid := createContainerRequest{Name: "nat01", Image: "images:alpine/3.24", CPULimit: "12%", MemoryLimitMb: 120, DiskLimitGb: 1}
	if err := validateCreateRequest(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.CPULimit = "0%"
	if err := validateCreateRequest(invalid); err == nil {
		t.Fatal("expected CPU validation error")
	}
}

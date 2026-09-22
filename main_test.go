package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestPrometheusLabel(t *testing.T) {
	if got := prometheusLabel("release\\\"1\n"); got != `release\\\"1` {
		t.Fatalf("prometheusLabel = %q", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	for key, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
	} {
		if got := response.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   string
		status int
	}{
		{name: "invalid", err: errors.New("invalid port"), code: "invalid_request", status: http.StatusBadRequest},
		{name: "conflict", err: errors.New("listen port already assigned"), code: "conflict", status: http.StatusConflict},
		{name: "runtime", err: errors.New("incus start: exit status 1"), code: "runtime_error", status: http.StatusBadGateway},
		{name: "internal", err: errors.New("database unavailable"), code: "internal_error", status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, code := classifyError(test.err)
			if status != test.status || code != test.code {
				t.Fatalf("classifyError = (%d, %q), want (%d, %q)", status, code, test.status, test.code)
			}
		})
	}
}

func TestVersionedAPIRewritesPath(t *testing.T) {
	var got string
	handler := versionedAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/containers", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || got != "/api/containers" {
		t.Fatalf("versioned API = status %d path %q", response.Code, got)
	}
}

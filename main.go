package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"natbox/internal/incus"
	"natbox/internal/store"
	"natbox/web"
)

type app struct {
	incus        *incus.Client
	store        *store.Store
	auth         *adminAuth
	startedAt    time.Time
	portMin      int
	portMax      int
	sshPortStart int
	sshPortEnd   int
}

// buildVersion is overridden by release builds with -ldflags.
var buildVersion = "dev"

type envelope struct {
	Success bool   `json:"success"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

type containerView struct {
	Name         string              `json:"name"`
	Status       string              `json:"status"`
	Type         string              `json:"type"`
	IPv4         string              `json:"ipv4,omitempty"`
	Memory       *resourceView       `json:"memory,omitempty"`
	Disk         *resourceView       `json:"disk,omitempty"`
	StartedAt    string              `json:"startedAt,omitempty"`
	PortForwards []incus.PortForward `json:"portForwards,omitempty"`
	Traffic      *trafficView        `json:"traffic,omitempty"`
}

type resourceView struct {
	UsageBytes int64 `json:"usageBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

type trafficView struct {
	RxBytes   int64 `json:"rxBytes"`
	TxBytes   int64 `json:"txBytes"`
	RxPackets int64 `json:"rxPackets"`
	TxPackets int64 `json:"txPackets"`
}

type hostView struct {
	MemoryTotalBytes       int64 `json:"memoryTotalBytes"`
	MemoryAvailableBytes   int64 `json:"memoryAvailableBytes"`
	MemoryAllocatableBytes int64 `json:"memoryAllocatableBytes"`
	SwapTotalBytes         int64 `json:"swapTotalBytes"`
	SwapAvailableBytes     int64 `json:"swapAvailableBytes"`
	DiskTotalBytes         int64 `json:"diskTotalBytes"`
	DiskAvailableBytes     int64 `json:"diskAvailableBytes"`
	DiskAllocatableBytes   int64 `json:"diskAllocatableBytes"`
	SuggestedByMemory      int64 `json:"suggestedByMemory"`
	SuggestedByDisk        int64 `json:"suggestedByDisk"`
}

type portPolicyView struct {
	PublicMin int   `json:"publicMin"`
	PublicMax int   `json:"publicMax"`
	SSHMin    int   `json:"sshMin"`
	SSHMax    int   `json:"sshMax"`
	UsedTCP   []int `json:"usedTcp,omitempty"`
	UsedUDP   []int `json:"usedUdp,omitempty"`
}

type templateView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	MemoryLimitMb int64  `json:"memoryLimitMb"`
	DiskLimitGb   int64  `json:"diskLimitGb"`
	CPULimit      string `json:"cpuLimit"`
}

var builtinTemplates = []templateView{
	{ID: "tiny", Name: "轻量 NAT", Image: "images:alpine/3.24", MemoryLimitMb: 120, DiskLimitGb: 1, CPULimit: "12%"},
	{ID: "standard", Name: "标准 NAT", Image: "images:alpine/3.24", MemoryLimitMb: 256, DiskLimitGb: 2, CPULimit: "25%"},
	{ID: "gateway", Name: "网关 NAT", Image: "images:alpine/3.24", MemoryLimitMb: 512, DiskLimitGb: 4, CPULimit: "50%"},
}

type createContainerRequest struct {
	Name, Image, CPULimit, PublicKey string
	MemoryLimitMb, DiskLimitGb       int64
}

type bulkCreateRequest struct {
	Prefix        string `json:"prefix"`
	StartIndex    int    `json:"startIndex"`
	Count         int    `json:"count"`
	Image         string `json:"image"`
	CPULimit      string `json:"cpuLimit"`
	MemoryLimitMb int64  `json:"memoryLimitMb"`
	DiskLimitGb   int64  `json:"diskLimitGb"`
	PublicKey     string `json:"publicKey"`
	AllocateSSH   bool   `json:"allocateSSH"`
}

type bulkCreateResult struct {
	Created []map[string]any `json:"created"`
	Failed  string           `json:"failed,omitempty"`
}

type policyRequest struct {
	QuotaBytes int64  `json:"quotaBytes"`
	ExpiresAt  string `json:"expiresAt"`
}

type policyView struct {
	QuotaBytes     int64  `json:"quotaBytes"`
	UsedBytes      int64  `json:"usedBytes"`
	RemainingBytes int64  `json:"remainingBytes"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	QuotaResetAt   string `json:"quotaResetAt,omitempty"`
	QuotaEnforced  bool   `json:"quotaEnforced"`
	ExpiryEnforced bool   `json:"expiryEnforced"`
}

func main() {
	client, err := incus.New(incus.CommandRunner{})
	if err != nil {
		log.Fatal(err)
	}
	database, err := store.Open(databasePath())
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	a := &app{incus: client, store: database, startedAt: time.Now().UTC()}
	authManager, err := newAdminAuth(os.Getenv("NATBOX_ADMIN_PASSWORD_HASH"))
	if err != nil {
		log.Fatal("invalid NATBOX_ADMIN_PASSWORD_HASH: ", err)
	}
	a.auth = authManager
	a.portMin = envInt("NATBOX_PORT_MIN", 1)
	a.portMax = envInt("NATBOX_PORT_MAX", 65535)
	a.sshPortStart = envInt("NATBOX_SSH_PORT_MIN", 2201)
	a.sshPortEnd = envInt("NATBOX_SSH_PORT_MAX", 2299)
	if err := validateRange(a.portMin, a.portMax, 1, 65535); err != nil {
		log.Fatal("invalid NATBOX_PORT_MIN/NATBOX_PORT_MAX: ", err)
	}
	if err := validateRange(a.sshPortStart, a.sshPortEnd, a.portMin, a.portMax); err != nil {
		log.Fatal("invalid NATBOX_SSH_PORT_MIN/NATBOX_SSH_PORT_MAX: ", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", a.health)
	mux.HandleFunc("/api/metrics", a.metrics)
	mux.HandleFunc("/api/diagnostics", a.diagnostics)
	mux.HandleFunc("/api/auth/status", a.authStatus)
	mux.HandleFunc("/api/auth/login", a.authLogin)
	mux.HandleFunc("/api/auth/logout", a.authLogout)
	mux.HandleFunc("/api/auth/me", a.authMe)
	mux.HandleFunc("/api/capabilities", a.capabilities)
	mux.HandleFunc("/api/host", a.host)
	mux.HandleFunc("/api/ports", a.ports)
	mux.HandleFunc("/api/templates", a.templates)
	mux.HandleFunc("/api/managed-containers", a.managedContainers)
	mux.HandleFunc("/api/audit", a.auditEvents)
	mux.HandleFunc("/api/backup", a.backup)
	mux.HandleFunc("/api/backup/restore", a.restoreBackup)
	mux.HandleFunc("/api/reconcile", a.reconcileAPI)
	mux.HandleFunc("/api/reconcile/repair", a.repairReconcileAPI)
	mux.HandleFunc("/api/containers", a.containers)
	mux.HandleFunc("/api/containers/", a.containerAction)
	// Keep the original /api paths for compatibility while exposing a stable
	// versioned prefix for new clients.
	mux.Handle("/api/v1/", versionedAPI(mux))
	mux.HandleFunc("/", serveIndex)
	listen := os.Getenv("NATBOX_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8787"
	}
	token := os.Getenv("NATBOX_TOKEN")
	if !isLoopbackListen(listen) && len(token) < 16 {
		log.Fatal("NATBOX_TOKEN must be at least 16 characters when NATBOX_LISTEN is not loopback")
	}
	certFile, keyFile := os.Getenv("NATBOX_TLS_CERT"), os.Getenv("NATBOX_TLS_KEY")
	if (certFile == "") != (keyFile == "") {
		log.Fatal("NATBOX_TLS_CERT and NATBOX_TLS_KEY must be set together")
	}
	if os.Getenv("NATBOX_REQUIRE_TLS") == "1" && certFile == "" {
		log.Fatal("NATBOX_REQUIRE_TLS=1 requires NATBOX_TLS_CERT and NATBOX_TLS_KEY")
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           securityHeaders(logging(a.authMiddleware(mux, token))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	runContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go a.enforcePolicies(runContext)
	a.startBackupScheduler(runContext)
	serverErrors := make(chan error, 1)
	if certFile != "" {
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		log.Printf("natbox listening on https://%s", listen)
		go func() { serverErrors <- server.ListenAndServeTLS(certFile, keyFile) }()
	} else {
		log.Printf("natbox listening on http://%s", listen)
		go func() { serverErrors <- server.ListenAndServe() }()
	}
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-runContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			log.Printf("natbox graceful shutdown: %v", err)
		}
	}
}

func databasePath() string {
	if path := strings.TrimSpace(os.Getenv("NATBOX_DB")); path != "" {
		return path
	}
	if runtime.GOOS == "linux" {
		return "/var/lib/natbox/natbox.db"
	}
	return "natbox.db"
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	write(w, envelope{Success: true, Data: map[string]any{"service": "natbox", "version": buildVersion, "uptimeSeconds": int64(time.Since(a.startedAt).Seconds())}})
}

func (a *app) metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	instances, err := a.incus.List(ctx)
	if err != nil {
		writeStatusCode(w, http.StatusServiceUnavailable, "runtime_unavailable", err.Error())
		return
	}
	managed, managedErr := a.store.ListContainers(ctx)
	if managedErr != nil {
		writeStatusCode(w, http.StatusServiceUnavailable, "database_unavailable", managedErr.Error())
		return
	}
	host, hostErr := readHostSnapshot()
	if hostErr != nil {
		writeStatusCode(w, http.StatusServiceUnavailable, "host_unavailable", hostErr.Error())
		return
	}
	running := 0
	for _, instance := range instances {
		if instance.Status == "Running" {
			running++
		}
	}
	policyCount := 0
	for _, item := range managed {
		if item.QuotaBytes > 0 || item.ExpiresAt != "" {
			policyCount++
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	f := func(name string, value any) { fmt.Fprintf(w, "%s %v\n", name, value) }
	f("natbox_up", 1)
	f("natbox_build_info{version=\""+prometheusLabel(buildVersion)+"\"}", 1)
	f("natbox_managed_containers", len(managed))
	f("natbox_runtime_containers", len(instances))
	f("natbox_running_containers", running)
	f("natbox_policy_enforced_containers", policyCount)
	f("natbox_host_memory_available_bytes", host.MemoryAvailableBytes)
	f("natbox_host_disk_available_bytes", host.DiskAvailableBytes)
}

func (a *app) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	result := map[string]any{"service": "natbox", "version": buildVersion, "uptimeSeconds": int64(time.Since(a.startedAt).Seconds())}
	capabilities, capErr := a.incus.Probe(ctx)
	result["runtime"] = capabilities
	if capErr != nil {
		result["runtimeError"] = capErr.Error()
	}
	if host, hostErr := readHostSnapshot(); hostErr == nil {
		result["host"] = a.hostView(host)
	} else {
		result["hostError"] = hostErr.Error()
	}
	if report, reconcileErr := a.reconcile(ctx); reconcileErr == nil {
		result["reconcile"] = report
	} else {
		result["reconcileError"] = reconcileErr.Error()
	}
	if _, ok := result["runtimeError"]; ok {
		write(w, envelope{Success: false, Message: "runtime diagnostics failed", Data: result})
		return
	}
	write(w, envelope{Success: true, Data: result})
}

func (a *app) authStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	a.auth.status(w)
}

func (a *app) authLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	a.auth.login(w, r, func(result, details string) {
		a.recordAudit(r, "auth.login", "admin", "", result, details)
	})
}

func (a *app) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	a.auth.logout(w, r, func(result, details string) {
		a.recordAudit(r, "auth.logout", "admin", "", result, details)
	})
}

func (a *app) authMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	a.auth.me(w, r)
}
func (a *app) capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	data, err := a.incus.Probe(ctx)
	respond(w, data, err)
}

func (a *app) host(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	snapshot, err := readHostSnapshot()
	if err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, a.hostView(snapshot), nil)
}

func (a *app) templates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	respond(w, builtinTemplates, nil)
}

func (a *app) managedContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := a.store.ListContainers(r.Context())
	respond(w, items, err)
}

func (a *app) auditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := a.store.ListAudit(r.Context(), limit, store.ParseBeforeID(r.URL.Query().Get("beforeId")))
	respond(w, events, err)
}

func (a *app) backup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	backup, err := a.store.Export(r.Context())
	if err != nil {
		respond(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="natbox-backup.json"`)
	write(w, envelope{Success: true, Data: backup})
}

func (a *app) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var request struct {
		Confirm string       `json:"confirm"`
		Backup  store.Backup `json:"backup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		respond(w, nil, err)
		return
	}
	if request.Confirm != "RESTORE" {
		respond(w, nil, fmt.Errorf(`confirm must be "RESTORE"`))
		return
	}
	if err := a.store.Restore(r.Context(), request.Backup); err != nil {
		a.recordAudit(r, "backup.restore", "database", "", "failure", err.Error())
		respond(w, nil, err)
		return
	}
	a.recordAudit(r, "backup.restore", "database", "", "success", fmt.Sprintf("containers=%d", len(request.Backup.Containers)))
	respond(w, map[string]any{"restoredContainers": len(request.Backup.Containers), "runtimeInstancesUntouched": true}, nil)
}

func (a *app) reconcileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	report, err := a.reconcile(r.Context())
	respond(w, report, err)
}

func (a *app) repairReconcileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	report, err := a.reconcile(ctx)
	if err != nil {
		respond(w, nil, err)
		return
	}
	results := make([]map[string]any, 0, len(report.DesiredStateDrift))
	for _, drift := range report.DesiredStateDrift {
		action := "stop"
		if drift.DesiredState == "running" {
			action = "start"
		}
		var actionErr error
		switch action {
		case "start":
			actionErr = a.incus.Start(ctx, drift.Name)
			if actionErr == nil {
				_, actionErr = a.incus.WaitIPv4(ctx, drift.Name)
			}
		case "stop":
			actionErr = a.incus.Stop(ctx, drift.Name)
		}
		result := "success"
		if actionErr != nil {
			result = "failure"
		} else if stateErr := a.store.SetDesiredState(ctx, drift.Name, drift.DesiredState); stateErr != nil && stateErr != sql.ErrNoRows {
			actionErr = stateErr
			result = "failure"
		}
		a.recordAudit(r, "reconcile."+action, "container", drift.Name, result, "desired-state repair")
		entry := map[string]any{"name": drift.Name, "action": action, "result": result}
		if actionErr != nil {
			entry["error"] = actionErr.Error()
		}
		results = append(results, entry)
	}
	respond(w, map[string]any{"repaired": results, "unmanagedUntouched": len(report.RuntimeUnmanaged), "missingUntouched": len(report.ManagedMissing)}, nil)
}

func (a *app) recordAudit(r *http.Request, action, target, targetID, result, details string) {
	if a.store == nil {
		return
	}
	if err := a.store.Audit(r.Context(), store.AuditEvent{Actor: requestActor(r), Action: action, Target: target, TargetID: targetID, Result: result, SourceIP: requestIP(r), Details: details}); err != nil {
		log.Printf("audit %s %s/%s: %v", action, target, targetID, err)
	}
}

func requestActor(r *http.Request) string {
	if r.Header.Get("Authorization") != "" {
		return "bearer-token"
	}
	return "local"
}

func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return ""
}

func (a *app) ports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	usedTCP, usedUDP, err := a.usedPorts(ctx)
	if err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, portPolicyView{PublicMin: a.portMin, PublicMax: a.portMax, SSHMin: a.sshPortStart, SSHMax: a.sshPortEnd, UsedTCP: usedTCP, UsedUDP: usedUDP}, nil)
}

func (a *app) containers(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/containers"), "/")
	parts := strings.Split(path, "/")
	if path == "bulk" && r.Method == http.MethodPost {
		a.bulkCreate(w, r)
		return
	}
	if path == "bulk/action" && r.Method == http.MethodPost {
		a.bulkAction(w, r)
		return
	}
	if path == "" && r.Method == http.MethodGet {
		ctx, cancel := timeout()
		defer cancel()
		instances, err := a.incus.List(ctx)
		if err != nil {
			respond(w, nil, err)
			return
		}
		data := make([]containerView, 0, len(instances))
		for _, item := range instances {
			forwards, forwardErr := a.incus.ListPortForwards(ctx, item.Name)
			if forwardErr != nil {
				log.Printf("list port forwards for %s: %v", item.Name, forwardErr)
				forwards = []incus.PortForward{}
			}
			view := containerView{Name: item.Name, Status: item.Status, Type: item.Type, IPv4: item.IPv4(), PortForwards: forwards}
			if info, infoErr := a.incus.Info(ctx, item.Name); infoErr == nil {
				view.StartedAt = info.StartedAt
				view.Memory = &resourceView{UsageBytes: info.Memory.Usage, TotalBytes: info.Memory.Total}
				if root, ok := info.Disk["root"]; ok {
					view.Disk = &resourceView{UsageBytes: root.Usage, TotalBytes: root.Total}
				}
			}
			if traffic, trafficErr := a.incus.Stats(ctx, item.Name); trafficErr == nil {
				view.Traffic = &trafficView{RxBytes: traffic.RxBytes, TxBytes: traffic.TxBytes, RxPackets: traffic.RxPackets, TxPackets: traffic.TxPackets}
			}
			data = append(data, view)
		}
		respond(w, data, nil)
		return
	}
	if path == "" && r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req createContainerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respond(w, nil, err)
			return
		}
		if req.Image == "" {
			req.Image = "images:alpine/3.24"
		}
		ctx, cancel := timeout()
		defer cancel()
		if err := a.checkCapacity(ctx, req.MemoryLimitMb, req.DiskLimitGb, 1); err != nil {
			respond(w, nil, err)
			return
		}
		err := a.createOne(ctx, req)
		if err == nil {
			err = a.persistManaged(ctx, req)
		}
		if err != nil {
			a.recordAudit(r, "container.create", "container", req.Name, "failure", "")
		} else {
			a.recordAudit(r, "container.create", "container", req.Name, "success", fmt.Sprintf("image=%s memoryLimitMb=%d diskLimitGb=%d cpuLimit=%s", req.Image, req.MemoryLimitMb, req.DiskLimitGb, req.CPULimit))
		}
		respond(w, map[string]string{"name": req.Name}, err)
		return
	}
	if len(parts) == 2 && parts[1] == "ports" && r.Method == http.MethodGet {
		ctx, cancel := timeout()
		defer cancel()
		data, err := a.incus.ListPortForwards(ctx, parts[0])
		respond(w, data, err)
		return
	}
	if len(parts) == 2 && parts[1] == "info" && r.Method == http.MethodGet {
		ctx, cancel := timeout()
		defer cancel()
		data, err := a.incus.Info(ctx, parts[0])
		respond(w, data, err)
		return
	}
	if len(parts) == 2 && parts[1] == "policy" {
		ctx, cancel := timeout()
		defer cancel()
		if r.Method == http.MethodGet {
			data, err := a.policy(ctx, parts[0])
			respond(w, data, err)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			var request policyRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				respond(w, nil, err)
				return
			}
			data, err := a.updatePolicy(ctx, parts[0], request)
			result := "success"
			if err != nil {
				result = "failure"
			}
			a.recordAudit(r, "container.policy", "container", parts[0], result, fmt.Sprintf("quotaBytes=%d expiresAt=%s", request.QuotaBytes, request.ExpiresAt))
			respond(w, data, err)
			return
		}
	}
	if len(parts) == 2 && parts[1] == "ssh-key" && r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var req struct {
			PublicKey string `json:"publicKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respond(w, nil, err)
			return
		}
		ctx, cancel := timeout()
		defer cancel()
		err := a.incus.ConfigureSSH(ctx, parts[0], req.PublicKey)
		respond(w, map[string]string{"name": parts[0]}, err)
		return
	}
	if len(parts) == 2 && parts[1] == "ports" && r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var req struct {
			Protocol   string `json:"protocol"`
			ListenPort int    `json:"listenPort"`
			TargetPort int    `json:"targetPort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respond(w, nil, err)
			return
		}
		if req.ListenPort < a.portMin || req.ListenPort > a.portMax {
			respond(w, nil, fmt.Errorf("listenPort must be within configured range %d-%d", a.portMin, a.portMax))
			return
		}
		ctx, cancel := timeout()
		defer cancel()
		err := a.incus.AddPortForward(ctx, parts[0], req.Protocol, req.ListenPort, req.TargetPort)
		result := "success"
		if err != nil {
			result = "failure"
		}
		a.recordAudit(r, "port.add", "container", parts[0], result, fmt.Sprintf("protocol=%s listenPort=%d targetPort=%d", req.Protocol, req.ListenPort, req.TargetPort))
		respond(w, map[string]any{"name": parts[0], "protocol": req.Protocol, "listenPort": req.ListenPort, "targetPort": req.TargetPort}, err)
		return
	}
	if len(parts) == 2 && parts[1] == "ports" && r.Method == http.MethodDelete {
		protocol := r.URL.Query().Get("protocol")
		listenPort, err := strconv.Atoi(r.URL.Query().Get("listenPort"))
		if err != nil {
			respond(w, nil, fmt.Errorf("invalid listenPort"))
			return
		}
		ctx, cancel := timeout()
		defer cancel()
		err = a.incus.RemovePortForward(ctx, parts[0], protocol, listenPort)
		result := "success"
		if err != nil {
			result = "failure"
		}
		a.recordAudit(r, "port.remove", "container", parts[0], result, fmt.Sprintf("protocol=%s listenPort=%d", protocol, listenPort))
		respond(w, map[string]any{"name": parts[0], "protocol": protocol, "listenPort": listenPort}, err)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	var err error
	switch parts[1] {
	case "start":
		err = a.incus.Start(ctx, parts[0])
		if err == nil {
			_, err = a.incus.WaitIPv4(ctx, parts[0])
		}
	case "stop":
		err = a.incus.Stop(ctx, parts[0])
	case "restart":
		err = a.incus.Restart(ctx, parts[0])
		if err == nil {
			_, err = a.incus.WaitIPv4(ctx, parts[0])
		}
	case "delete":
		err = a.incus.Delete(ctx, parts[0])
		if err == nil {
			err = a.store.DeleteContainer(ctx, parts[0])
		}
	default:
		http.NotFound(w, r)
		return
	}
	result := "success"
	if err != nil {
		result = "failure"
	} else if parts[1] == "start" || parts[1] == "restart" || parts[1] == "stop" {
		state := "stopped"
		if parts[1] != "stop" {
			state = "running"
		}
		if updateErr := a.store.SetDesiredState(ctx, parts[0], state); updateErr != nil && updateErr != sql.ErrNoRows {
			log.Printf("update managed state for %s: %v", parts[0], updateErr)
		}
	}
	a.recordAudit(r, "container."+parts[1], "container", parts[0], result, "")
	respond(w, map[string]string{"name": parts[0], "action": parts[1]}, err)
}

func (a *app) bulkCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req bulkCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond(w, nil, err)
		return
	}
	if req.Image == "" {
		req.Image = "images:alpine/3.24"
	}
	if err := validateCreateRequest(createContainerRequest{Name: "nat01", Image: req.Image, CPULimit: req.CPULimit, MemoryLimitMb: req.MemoryLimitMb, DiskLimitGb: req.DiskLimitGb, PublicKey: req.PublicKey}); err != nil {
		respond(w, nil, err)
		return
	}
	names, err := bulkNames(req.Prefix, req.StartIndex, req.Count)
	if err != nil {
		respond(w, nil, err)
		return
	}
	instances, err := a.incus.List(r.Context())
	if err != nil {
		respond(w, nil, fmt.Errorf("preflight list: %w", err))
		return
	}
	existing := make(map[string]bool, len(instances))
	for _, item := range instances {
		existing[item.Name] = true
	}
	for _, name := range names {
		if existing[name] {
			respond(w, nil, fmt.Errorf("container %q already exists; nothing was created", name))
			return
		}
	}
	if req.AllocateSSH {
		if err := a.checkSSHCapacity(r.Context(), len(names)); err != nil {
			respond(w, nil, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := a.checkCapacity(ctx, req.MemoryLimitMb, req.DiskLimitGb, int64(len(names))); err != nil {
		respond(w, nil, err)
		return
	}
	result := bulkCreateResult{Created: make([]map[string]any, 0, len(names))}
	for _, name := range names {
		createReq := createContainerRequest{Name: name, Image: req.Image, CPULimit: req.CPULimit, MemoryLimitMb: req.MemoryLimitMb, DiskLimitGb: req.DiskLimitGb, PublicKey: req.PublicKey}
		if err := a.createOne(ctx, createReq); err != nil {
			a.recordAudit(r, "container.create", "container", name, "failure", "runtime create")
			result.Failed = name
			write(w, envelope{Success: false, Message: fmt.Sprintf("批量创建在 %s 处失败，已创建实例未回滚: %v", name, err), Data: result})
			return
		}
		if err := a.persistManaged(ctx, createReq); err != nil {
			result.Failed = name
			a.recordAudit(r, "container.create", "container", name, "failure", "metadata persistence")
			write(w, envelope{Success: false, Message: fmt.Sprintf("实例 %s 已创建，但管理数据保存失败，已创建实例未回滚: %v", name, err), Data: result})
			return
		}
		a.recordAudit(r, "container.create", "container", name, "success", fmt.Sprintf("image=%s memoryLimitMb=%d diskLimitGb=%d cpuLimit=%s", createReq.Image, createReq.MemoryLimitMb, createReq.DiskLimitGb, createReq.CPULimit))
		entry := map[string]any{"name": name}
		if req.AllocateSSH {
			forward, sshErr := a.allocateSSH(ctx, name)
			if sshErr != nil {
				a.recordAudit(r, "port.allocate_ssh", "container", name, "failure", "batch allocation")
				result.Failed = name
				write(w, envelope{Success: false, Message: fmt.Sprintf("实例 %s 已创建，但 SSH 端口分配失败，已创建实例未回滚: %v", name, sshErr), Data: result})
				return
			}
			entry["ssh"] = forward
		}
		result.Created = append(result.Created, entry)
	}
	respond(w, result, nil)
}

func (a *app) bulkAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var request struct {
		Names  []string `json:"names"`
		Action string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		respond(w, nil, err)
		return
	}
	if len(request.Names) < 1 || len(request.Names) > 32 {
		respond(w, nil, errors.New("names must contain between 1 and 32 containers"))
		return
	}
	switch request.Action {
	case "start", "stop", "restart", "delete":
	default:
		respond(w, nil, errors.New("action must be start, stop, restart, or delete"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	seen := make(map[string]bool, len(request.Names))
	results := make([]map[string]any, 0, len(request.Names))
	for _, name := range request.Names {
		if seen[name] {
			continue
		}
		seen[name] = true
		var actionErr error
		switch request.Action {
		case "start":
			actionErr = a.incus.Start(ctx, name)
			if actionErr == nil {
				_, actionErr = a.incus.WaitIPv4(ctx, name)
			}
		case "stop":
			actionErr = a.incus.Stop(ctx, name)
		case "restart":
			actionErr = a.incus.Restart(ctx, name)
			if actionErr == nil {
				_, actionErr = a.incus.WaitIPv4(ctx, name)
			}
		case "delete":
			actionErr = a.incus.Delete(ctx, name)
			if actionErr == nil {
				actionErr = a.store.DeleteContainer(ctx, name)
			}
		}
		result := "success"
		if actionErr != nil {
			result = "failure"
		} else if request.Action != "delete" {
			desired := "stopped"
			if request.Action != "stop" {
				desired = "running"
			}
			if stateErr := a.store.SetDesiredState(ctx, name, desired); stateErr != nil && stateErr != sql.ErrNoRows {
				actionErr = stateErr
				result = "failure"
			}
		}
		a.recordAudit(r, "container."+request.Action, "container", name, result, "bulk action")
		entry := map[string]any{"name": name, "action": request.Action, "result": result}
		if actionErr != nil {
			entry["error"] = actionErr.Error()
		}
		results = append(results, entry)
	}
	respond(w, map[string]any{"results": results}, nil)
}

func (a *app) persistManaged(ctx context.Context, req createContainerRequest) error {
	if a.store == nil {
		return errors.New("management store is unavailable")
	}
	return a.store.UpsertContainer(ctx, store.ManagedContainer{Name: req.Name, Image: req.Image, MemoryLimitMb: req.MemoryLimitMb, DiskLimitGb: req.DiskLimitGb, CPULimit: req.CPULimit, DesiredState: "running"})
}

func (a *app) createOne(ctx context.Context, req createContainerRequest) error {
	if err := validateCreateRequest(req); err != nil {
		return err
	}
	if err := a.incus.Create(ctx, incus.InstanceSpec{Name: req.Name, Image: req.Image, CPUAllowance: req.CPULimit, MemoryBytes: req.MemoryLimitMb * 1024 * 1024, RootDiskBytes: req.DiskLimitGb * 1024 * 1024 * 1024}); err != nil {
		return err
	}
	if _, err := a.incus.WaitIPv4(ctx, req.Name); err != nil {
		return err
	}
	if strings.TrimSpace(req.PublicKey) != "" {
		return a.incus.ConfigureSSH(ctx, req.Name, req.PublicKey)
	}
	return nil
}

func validateCreateRequest(req createContainerRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(req.Image) == "" {
		return fmt.Errorf("image is required")
	}
	if req.MemoryLimitMb < 16 || req.MemoryLimitMb > 8192 {
		return fmt.Errorf("memoryLimitMb must be between 16 and 8192")
	}
	if req.DiskLimitGb < 1 || req.DiskLimitGb > 100 {
		return fmt.Errorf("diskLimitGb must be between 1 and 100")
	}
	if req.CPULimit != "" && !regexp.MustCompile(`^[1-9][0-9]{0,2}%$`).MatchString(req.CPULimit) {
		return fmt.Errorf("cpuLimit must be a percentage such as 12%%")
	}
	return nil
}

var bulkPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,58}$`)

func bulkNames(prefix string, start, count int) ([]string, error) {
	if !bulkPrefixPattern.MatchString(prefix) {
		return nil, fmt.Errorf("invalid prefix: use letters, numbers, dot, or hyphen")
	}
	if start < 0 || start > 999999 {
		return nil, fmt.Errorf("startIndex must be between 0 and 999999")
	}
	if count < 1 || count > 32 {
		return nil, fmt.Errorf("count must be between 1 and 32")
	}
	last := start + count - 1
	width := 2
	if last >= 100 {
		width = len(strconv.Itoa(last))
	}
	names := make([]string, 0, count)
	seen := make(map[string]bool, count)
	for i := start; i <= last; i++ {
		name := fmt.Sprintf("%s%0*d", prefix, width, i)
		if len(name) > 63 || seen[name] {
			return nil, fmt.Errorf("generated container name %q is invalid or duplicated", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

func (a *app) containerAction(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/containers/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "ssh" || r.Method != http.MethodPost {
		a.containers(w, r)
		return
	}
	name := parts[0]
	ctx, cancel := timeout()
	defer cancel()
	forward, err := a.allocateSSH(ctx, name)
	result := "success"
	if err != nil {
		result = "failure"
	}
	a.recordAudit(r, "port.allocate_ssh", "container", name, result, func() string {
		if err != nil {
			return ""
		}
		return fmt.Sprintf("listenPort=%d targetPort=%d", forward.ListenPort, forward.TargetPort)
	}())
	respond(w, forward, err)
}

func (a *app) allocateSSH(ctx context.Context, name string) (incus.PortForward, error) {
	forwards, err := a.incus.ListPortForwards(ctx, name)
	if err != nil {
		return incus.PortForward{}, err
	}
	for _, forward := range forwards {
		if forward.Protocol == "tcp" && forward.TargetPort == 22 {
			return forward, nil
		}
	}
	used := make(map[int]bool)
	instances, err := a.incus.List(ctx)
	if err != nil {
		return incus.PortForward{}, err
	}
	for _, instance := range instances {
		instanceForwards, listErr := a.incus.ListPortForwards(ctx, instance.Name)
		if listErr != nil {
			return incus.PortForward{}, listErr
		}
		for _, forward := range instanceForwards {
			if forward.Protocol == "tcp" {
				used[forward.ListenPort] = true
			}
		}
	}
	for port := a.sshPortStart; port <= a.sshPortEnd; port++ {
		if used[port] {
			continue
		}
		if err := a.incus.AddPortForward(ctx, name, "tcp", port, 22); err != nil {
			return incus.PortForward{}, err
		}
		return incus.PortForward{Name: name, Protocol: "tcp", ListenPort: port, TargetPort: 22}, nil
	}
	return incus.PortForward{}, fmt.Errorf("no free SSH port in range %d-%d", a.sshPortStart, a.sshPortEnd)
}

func (a *app) checkSSHCapacity(ctx context.Context, needed int) error {
	used := make(map[int]bool)
	instances, err := a.incus.List(ctx)
	if err != nil {
		return fmt.Errorf("preflight SSH ports: %w", err)
	}
	for _, instance := range instances {
		forwards, listErr := a.incus.ListPortForwards(ctx, instance.Name)
		if listErr != nil {
			return fmt.Errorf("preflight SSH ports: %w", listErr)
		}
		for _, forward := range forwards {
			if forward.Protocol == "tcp" && forward.ListenPort >= a.sshPortStart && forward.ListenPort <= a.sshPortEnd {
				used[forward.ListenPort] = true
			}
		}
	}
	available := a.sshPortEnd - a.sshPortStart + 1 - len(used)
	if needed > available {
		return fmt.Errorf("not enough free SSH ports in range 2201-2299: need %d, available %d", needed, available)
	}
	return nil
}

func (a *app) usedPorts(ctx context.Context) ([]int, []int, error) {
	instances, err := a.incus.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	tcpSet, udpSet := map[int]bool{}, map[int]bool{}
	for _, instance := range instances {
		forwards, listErr := a.incus.ListPortForwards(ctx, instance.Name)
		if listErr != nil {
			return nil, nil, listErr
		}
		for _, forward := range forwards {
			switch forward.Protocol {
			case "tcp":
				tcpSet[forward.ListenPort] = true
			case "udp":
				udpSet[forward.ListenPort] = true
			}
		}
	}
	return sortedPorts(tcpSet), sortedPorts(udpSet), nil
}

func sortedPorts(values map[int]bool) []int {
	result := make([]int, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func (a *app) checkCapacity(ctx context.Context, memoryMb, diskGb, count int64) error {
	if count < 1 {
		return fmt.Errorf("count must be positive")
	}
	snapshot, err := readHostSnapshot()
	if err != nil {
		return err
	}
	view := a.hostView(snapshot)
	requiredMemory, requiredDisk := memoryMb*1024*1024*count, diskGb*1024*1024*1024*count
	if requiredMemory > view.MemoryAllocatableBytes {
		return fmt.Errorf("requested memory %s exceeds host allocatable memory %s", formatBytes(requiredMemory), formatBytes(view.MemoryAllocatableBytes))
	}
	if requiredDisk > view.DiskAllocatableBytes {
		return fmt.Errorf("requested disk %s exceeds host allocatable disk %s", formatBytes(requiredDisk), formatBytes(view.DiskAllocatableBytes))
	}
	return nil
}

func (a *app) hostView(snapshot hostSnapshot) hostView {
	memoryReserve := int64(envInt("NATBOX_MEMORY_RESERVE_MB", 256)) * 1024 * 1024
	diskReserve := int64(envInt("NATBOX_DISK_RESERVE_GB", 1)) * 1024 * 1024 * 1024
	memoryAlloc := snapshot.MemoryAvailableBytes - memoryReserve
	diskAlloc := snapshot.DiskAvailableBytes - diskReserve
	if memoryAlloc < 0 {
		memoryAlloc = 0
	}
	if diskAlloc < 0 {
		diskAlloc = 0
	}
	return hostView{
		MemoryTotalBytes: snapshot.MemoryTotalBytes, MemoryAvailableBytes: snapshot.MemoryAvailableBytes, MemoryAllocatableBytes: memoryAlloc,
		SwapTotalBytes: snapshot.SwapTotalBytes, SwapAvailableBytes: snapshot.SwapAvailableBytes,
		DiskTotalBytes: snapshot.DiskTotalBytes, DiskAvailableBytes: snapshot.DiskAvailableBytes, DiskAllocatableBytes: diskAlloc,
		SuggestedByMemory: memoryAlloc / (120 * 1024 * 1024), SuggestedByDisk: diskAlloc / (1 * 1024 * 1024 * 1024),
	}
}

func formatBytes(value int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	amount := float64(value)
	for index, unit := range units {
		if index == 0 {
			if amount < 1024 {
				return strconv.FormatInt(value, 10) + " B"
			}
			continue
		}
		amount /= 1024
		if amount < 1024 || index == len(units)-1 {
			return fmt.Sprintf("%.1f %s", amount, unit)
		}
	}
	return strconv.FormatInt(value, 10) + " B"
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func validateRange(minimum, maximum, lower, upper int) error {
	if minimum < lower || maximum > upper || minimum > maximum {
		return fmt.Errorf("range must be between %d and %d", lower, upper)
	}
	return nil
}

func timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 45*time.Second)
}
func respond(w http.ResponseWriter, data any, err error) {
	if err != nil {
		status, code := classifyError(err)
		writeStatusCode(w, status, code, err.Error())
		return
	}
	write(w, envelope{Success: true, Data: data})
}
func write(w http.ResponseWriter, v envelope) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func method(w http.ResponseWriter) { http.Error(w, "method not allowed", http.StatusMethodNotAllowed) }
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (a *app) authMiddleware(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiPath := canonicalAPIPath(r.URL.Path)
		if !strings.HasPrefix(apiPath, "/api/") || apiPath == "/api/health" || apiPath == "/api/auth/status" || apiPath == "/api/auth/login" {
			next.ServeHTTP(w, r)
			return
		}
		bearerOK := token != "" && r.Header.Get("Authorization") == "Bearer "+token
		if bearerOK {
			next.ServeHTTP(w, r)
			return
		}
		if a.auth.enabled() {
			if _, ok := a.auth.session(r); !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="natbox"`)
				writeStatusCode(w, http.StatusUnauthorized, "auth_required", "authentication required")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && apiPath != "/api/auth/logout" && !a.auth.csrfValid(r) {
				writeStatusCode(w, http.StatusForbidden, "csrf_required", "csrf token required")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if token != "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="natbox"`)
			writeStatusCode(w, http.StatusUnauthorized, "auth_required", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func canonicalAPIPath(path string) string {
	if strings.HasPrefix(path, "/api/v1/") {
		return "/api" + strings.TrimPrefix(path, "/api/v1")
	}
	return path
}

func writeStatus(w http.ResponseWriter, status int, message string) {
	writeStatusCode(w, status, "request_error", message)
}

func writeStatusCode(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Success: false, Code: code, Message: message})
}

func classifyError(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "not managed") {
		return http.StatusNotFound, "not_found"
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"already exists", "already assigned", "duplicate", "conflict"} {
		if strings.Contains(message, marker) {
			return http.StatusConflict, "conflict"
		}
	}
	for _, marker := range []string{"required", "invalid", "must be", "between", "unsupported", "confirm must", "one line"} {
		if strings.Contains(message, marker) {
			return http.StatusBadRequest, "invalid_request"
		}
	}
	if strings.Contains(message, "incus ") || strings.Contains(message, "runtime ") || strings.Contains(message, "waiting for") {
		return http.StatusBadGateway, "runtime_error"
	}
	return http.StatusInternalServerError, "internal_error"
}

func versionedAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/api" + path
		next.ServeHTTP(w, clone)
	})
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" || host == "" {
		return host == "localhost"
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", "")
}
func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(web.IndexHTML)
}

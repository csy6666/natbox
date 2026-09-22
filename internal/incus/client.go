package incus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Runner interface {
	Run(context.Context, ...string) (string, string, error)
}

type CommandRunner struct{ Binary string }

func (r CommandRunner) Run(ctx context.Context, args ...string) (string, string, error) {
	bin := r.Binary
	if bin == "" {
		bin = os.Getenv("NATBOX_RUNTIME")
	}
	if bin == "" {
		if _, err := exec.LookPath("incus"); err == nil {
			bin = "incus"
		} else {
			bin = "lxc"
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type Client struct{ runner Runner }

func New(r Runner) (*Client, error) {
	if r == nil {
		return nil, errors.New("nil incus runner")
	}
	return &Client{runner: r}, nil
}

type Capabilities struct {
	Installed      bool     `json:"installed"`
	Version        string   `json:"version,omitempty"`
	StorageDrivers []string `json:"storageDrivers,omitempty"`
	Networks       []string `json:"networks,omitempty"`
}

func (c *Client) Probe(ctx context.Context) (Capabilities, error) {
	out, stderr, err := c.run(ctx, "version", "--format=json")
	cap := Capabilities{}
	if err != nil {
		// LXD 5.x exposes `lxc version` as human-readable text and rejects
		// Incus' JSON flag. Keep the fallback narrow and only use it when the
		// plain version command succeeds.
		plain, plainErr, plainRunErr := c.run(ctx, "version")
		if plainRunErr != nil {
			return Capabilities{}, fmt.Errorf("runtime version: %w: %s", err, strings.TrimSpace(stderr))
		}
		version := strings.TrimSpace(strings.SplitN(plain, "\n", 2)[0])
		if prefix, value, ok := strings.Cut(version, ":"); ok && strings.EqualFold(strings.TrimSpace(prefix), "client version") {
			version = strings.TrimSpace(value)
		}
		if version == "" {
			return Capabilities{}, fmt.Errorf("runtime version: empty response: %s", strings.TrimSpace(plainErr))
		}
		cap = Capabilities{Installed: true, Version: version}
	} else {
		var v struct {
			ServerVersion string `json:"server_version"`
			ClientVersion string `json:"client_version"`
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			return Capabilities{}, fmt.Errorf("invalid runtime version response: %w", err)
		}
		cap = Capabilities{Installed: true, Version: v.ServerVersion}
		if cap.Version == "" {
			cap.Version = v.ClientVersion
		}
	}
	if out, _, err := c.run(ctx, "storage", "list", "--format=json"); err == nil {
		var rows []struct {
			Driver string `json:"driver"`
		}
		if json.Unmarshal([]byte(out), &rows) == nil {
			for _, row := range rows {
				if row.Driver != "" {
					cap.StorageDrivers = append(cap.StorageDrivers, row.Driver)
				}
			}
		}
	}
	if out, _, err := c.run(ctx, "network", "list", "--format=json"); err == nil {
		var rows []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(out), &rows) == nil {
			for _, row := range rows {
				if row.Name != "" {
					cap.Networks = append(cap.Networks, row.Name)
				}
			}
		}
	}
	return cap, nil
}

type Instance struct {
	Name   string        `json:"name"`
	Status string        `json:"status"`
	Type   string        `json:"type"`
	State  InstanceState `json:"state,omitempty"`
}

type InstanceState struct {
	Network map[string]NetworkState `json:"network,omitempty"`
}

type InstanceInfo struct {
	Status     string               `json:"status"`
	StatusCode int                  `json:"status_code"`
	CreatedAt  string               `json:"created_at"`
	StartedAt  string               `json:"started_at"`
	Memory     ResourceUsage        `json:"memory"`
	Disk       map[string]DiskUsage `json:"disk"`
}

type ResourceUsage struct {
	Usage int64 `json:"usage"`
	Total int64 `json:"total"`
}

type DiskUsage struct {
	Usage int64 `json:"usage"`
	Total int64 `json:"total"`
}

type NetworkStats struct {
	RxBytes   int64 `json:"rx_bytes"`
	TxBytes   int64 `json:"tx_bytes"`
	RxPackets int64 `json:"rx_packets"`
	TxPackets int64 `json:"tx_packets"`
}

type NetworkState struct {
	Addresses []NetworkAddress `json:"addresses,omitempty"`
}

type NetworkAddress struct {
	Family  string `json:"family"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

func (i Instance) IPv4() string {
	for _, network := range i.State.Network {
		for _, address := range network.Addresses {
			if address.Family == "inet" && address.Scope != "link" && address.Address != "127.0.0.1" {
				return address.Address
			}
		}
	}
	return ""
}

func (c *Client) List(ctx context.Context) ([]Instance, error) {
	out, stderr, err := c.run(ctx, "list", "--format=json")
	if err != nil {
		return nil, commandError("list", err, stderr)
	}
	var rows []Instance
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("invalid incus list response: %w", err)
	}
	return rows, nil
}

func (c *Client) Info(ctx context.Context, name string) (InstanceInfo, error) {
	if err := validateName(name); err != nil {
		return InstanceInfo{}, err
	}
	out, stderr, err := c.run(ctx, "info", name, "--format=json")
	if err != nil {
		// LXD exposes the same state through its stable REST query endpoint,
		// while `lxc info --format=json` is not supported.
		queryOut, queryErrOut, queryErr := c.run(ctx, "query", "/1.0/instances/"+name+"/state")
		if queryErr != nil {
			return InstanceInfo{}, commandError("info", err, strings.TrimSpace(stderr)+"; "+strings.TrimSpace(queryErrOut))
		}
		var info InstanceInfo
		if decodeErr := json.Unmarshal([]byte(queryOut), &info); decodeErr != nil {
			return InstanceInfo{}, fmt.Errorf("invalid runtime state response: %w", decodeErr)
		}
		return info, nil
	}
	return decodeJSONInfo(out)
}

func (c *Client) Stats(ctx context.Context, name string) (NetworkStats, error) {
	if err := validateName(name); err != nil {
		return NetworkStats{}, err
	}
	out, stderr, err := c.run(ctx, "query", "/1.0/instances/"+name+"/state")
	if err != nil {
		return NetworkStats{}, commandError("network stats", err, stderr)
	}
	var state struct {
		Network map[string]struct {
			Counters struct {
				BytesReceived   int64 `json:"bytes_received"`
				BytesSent       int64 `json:"bytes_sent"`
				PacketsReceived int64 `json:"packets_received"`
				PacketsSent     int64 `json:"packets_sent"`
			} `json:"counters"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return NetworkStats{}, fmt.Errorf("invalid network state response: %w", err)
	}
	var stats NetworkStats
	for name, network := range state.Network {
		if name == "lo" {
			continue
		}
		stats.RxBytes += network.Counters.BytesReceived
		stats.TxBytes += network.Counters.BytesSent
		stats.RxPackets += network.Counters.PacketsReceived
		stats.TxPackets += network.Counters.PacketsSent
	}
	return stats, nil
}

func decodeJSONInfo(out string) (InstanceInfo, error) {
	var info InstanceInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return InstanceInfo{}, fmt.Errorf("invalid runtime info response: %w", err)
	}
	return info, nil
}

type InstanceSpec struct {
	Name, Image, CPUAllowance  string
	MemoryBytes, RootDiskBytes int64
}

type PortForward struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	ListenPort int    `json:"listenPort"`
	TargetPort int    `json:"targetPort"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)

func (s InstanceSpec) validate() error {
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("invalid instance name %q", s.Name)
	}
	if strings.TrimSpace(s.Image) == "" {
		return errors.New("image is required")
	}
	if s.MemoryBytes < 16*1024*1024 || s.MemoryBytes > 8*1024*1024*1024 {
		return errors.New("memory must be between 16 MiB and 8 GiB")
	}
	if s.RootDiskBytes < 128*1024*1024 || s.RootDiskBytes > 100*1024*1024*1024 {
		return errors.New("disk must be between 128 MiB and 100 GiB")
	}
	if s.CPUAllowance != "" && !regexp.MustCompile(`^[1-9][0-9]{0,2}%$`).MatchString(s.CPUAllowance) {
		return errors.New("invalid CPU allowance")
	}
	return nil
}

func (c *Client) Create(ctx context.Context, s InstanceSpec) error {
	if err := s.validate(); err != nil {
		return err
	}
	args := []string{"init", s.Image, s.Name, "--config", "limits.memory=" + bytesValue(s.MemoryBytes)}
	if s.CPUAllowance != "" {
		args = append(args, "--config", "limits.cpu.allowance="+s.CPUAllowance)
	}
	if _, stderr, err := c.run(ctx, args...); err != nil {
		return commandError("init", err, stderr)
	}
	if _, stderr, err := c.run(ctx, "config", "device", "override", s.Name, "root", "size="+bytesValue(s.RootDiskBytes)); err != nil {
		return commandError("set disk", err, stderr)
	}
	if _, stderr, err := c.run(ctx, "config", "set", s.Name, "boot.autostart", "true"); err != nil {
		return commandError("enable autostart", err, stderr)
	}
	if _, stderr, err := c.run(ctx, "start", s.Name); err != nil {
		return commandError("start", err, stderr)
	}
	return nil
}

func (c *Client) WaitIPv4(ctx context.Context, name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		instances, err := c.List(ctx)
		if err != nil {
			return "", err
		}
		for _, instance := range instances {
			if instance.Name == name {
				if address := instance.IPv4(); address != "" {
					return address, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for %s IPv4: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *Client) Start(ctx context.Context, name string) error {
	return c.lifecycle(ctx, "start", name)
}
func (c *Client) Stop(ctx context.Context, name string) error { return c.lifecycle(ctx, "stop", name) }
func (c *Client) Restart(ctx context.Context, name string) error {
	return c.lifecycle(ctx, "restart", name)
}
func (c *Client) Delete(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if _, stderr, err := c.run(ctx, "delete", name, "--force"); err != nil {
		return commandError("delete", err, stderr)
	}
	return nil
}

// ConfigureSSH installs one authorized key and enables sshd in an Alpine
// instance. The key is validated and encoded before being passed as an exec
// argument; it is never interpolated into a shell script.
func (c *Client) ConfigureSSH(ctx context.Context, name, publicKey string) error {
	if err := validateName(name); err != nil {
		return err
	}
	key, err := validatePublicKey(publicKey)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(key))
	const script = `set -eu
apk add --no-cache openssh-server
install -d -m 700 /root/.ssh
printf '%s' "$1" | base64 -d > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
rc-update add sshd default || true
rc-service sshd start || rc-service sshd restart`
	if _, stderr, err := c.run(ctx, "exec", name, "--", "sh", "-c", script, "natbox", encoded); err != nil {
		return commandError("configure ssh", err, stderr)
	}
	return nil
}

func (c *Client) AddPortForward(ctx context.Context, name, protocol string, listenPort, targetPort int) error {
	if err := validateName(name); err != nil {
		return err
	}
	if protocol != "tcp" && protocol != "udp" {
		return errors.New("protocol must be tcp or udp")
	}
	if listenPort < 1 || listenPort > 65535 || targetPort < 1 || targetPort > 65535 {
		return errors.New("invalid port")
	}
	forwards, err := c.ListPortForwards(ctx, name)
	if err != nil {
		return err
	}
	for _, forward := range forwards {
		if forward.Protocol == protocol && forward.ListenPort == listenPort {
			return fmt.Errorf("listen port %s/%d is already assigned", protocol, listenPort)
		}
	}
	device := fmt.Sprintf("proxy-%s-%d", protocol, listenPort)
	listen := fmt.Sprintf("%s:0.0.0.0:%d", protocol, listenPort)
	instances, err := c.List(ctx)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.Name == name {
			continue
		}
		otherForwards, listErr := c.ListPortForwards(ctx, instance.Name)
		if listErr != nil {
			return listErr
		}
		for _, forward := range otherForwards {
			if forward.Protocol == protocol && forward.ListenPort == listenPort {
				return fmt.Errorf("listen port %s/%d is already assigned to %s", protocol, listenPort, instance.Name)
			}
		}
	}
	targetAddress := ""
	for _, instance := range instances {
		if instance.Name == name {
			targetAddress = instance.IPv4()
			break
		}
	}
	if targetAddress == "" {
		return errors.New("container has no IPv4 address yet; start it and retry")
	}
	connect := fmt.Sprintf("%s:%s:%d", protocol, targetAddress, targetPort)
	if _, stderr, err := c.run(ctx, "config", "device", "add", name, device, "proxy", "listen="+listen, "connect="+connect); err != nil {
		return commandError("add port forward", err, stderr)
	}
	return nil
}

func (c *Client) ListPortForwards(ctx context.Context, name string) ([]PortForward, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	out, stderr, err := c.run(ctx, "config", "device", "show", name, "--format=json")
	if err != nil {
		queryOut, queryErrOut, queryErr := c.run(ctx, "query", "/1.0/instances/"+name)
		if queryErr != nil {
			return nil, commandError("list port forwards", err, strings.TrimSpace(stderr)+"; "+strings.TrimSpace(queryErrOut))
		}
		var instance struct {
			Devices map[string]struct {
				Type    string `json:"type"`
				Listen  string `json:"listen"`
				Connect string `json:"connect"`
			} `json:"devices"`
		}
		if decodeErr := json.Unmarshal([]byte(queryOut), &instance); decodeErr != nil {
			return nil, fmt.Errorf("invalid runtime device response: %w", decodeErr)
		}
		return portForwardsFromDevices(name, instance.Devices), nil
	}
	var devices map[string]struct {
		Type    string `json:"type"`
		Listen  string `json:"listen"`
		Connect string `json:"connect"`
	}
	if err := json.Unmarshal([]byte(out), &devices); err != nil {
		return nil, fmt.Errorf("invalid device response: %w", err)
	}
	return portForwardsFromDevices(name, devices), nil
}

func portForwardsFromDevices(name string, devices map[string]struct {
	Type    string `json:"type"`
	Listen  string `json:"listen"`
	Connect string `json:"connect"`
}) []PortForward {
	result := make([]PortForward, 0)
	for _, device := range devices {
		if device.Type != "proxy" {
			continue
		}
		protocol, listenPort, ok := parseEndpoint(device.Listen)
		if !ok {
			continue
		}
		targetProtocol, targetPort, ok := parseEndpoint(device.Connect)
		if !ok || targetProtocol != protocol {
			continue
		}
		result = append(result, PortForward{Name: name, Protocol: protocol, ListenPort: listenPort, TargetPort: targetPort})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ListenPort == result[j].ListenPort {
			return result[i].Protocol < result[j].Protocol
		}
		return result[i].ListenPort < result[j].ListenPort
	})
	return result
}

func (c *Client) RemovePortForward(ctx context.Context, name, protocol string, listenPort int) error {
	if err := validatePort(protocol, listenPort); err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	device := fmt.Sprintf("proxy-%s-%d", protocol, listenPort)
	if _, stderr, err := c.run(ctx, "config", "device", "remove", name, device); err != nil {
		return commandError("remove port forward", err, stderr)
	}
	return nil
}

func (c *Client) lifecycle(ctx context.Context, op, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if _, stderr, err := c.run(ctx, op, name); err != nil {
		return commandError(op, err, stderr)
	}
	return nil
}
func (c *Client) run(ctx context.Context, args ...string) (string, string, error) {
	return c.runner.Run(ctx, args...)
}
func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid instance name %q", name)
	}
	return nil
}

func validatePublicKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" || len(key) > 8192 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("public key must be one line and no longer than 8192 bytes")
	}
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return "", errors.New("invalid public key format")
	}
	allowed := map[string]bool{
		"ssh-ed25519":         true,
		"ssh-rsa":             true,
		"ecdsa-sha2-nistp256": true,
		"ecdsa-sha2-nistp384": true,
		"ecdsa-sha2-nistp521": true,
	}
	if !allowed[fields[0]] {
		return "", errors.New("unsupported public key type")
	}
	if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
		return "", errors.New("invalid public key encoding")
	}
	return key, nil
}
func commandError(op string, err error, stderr string) error {
	return fmt.Errorf("incus %s: %w: %s", op, err, strings.TrimSpace(stderr))
}

func validatePort(protocol string, listenPort int) error {
	if protocol != "tcp" && protocol != "udp" {
		return errors.New("protocol must be tcp or udp")
	}
	if listenPort < 1 || listenPort > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

func parseEndpoint(value string) (string, int, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return "", 0, false
	}
	port, err := strconv.Atoi(parts[2])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	return parts[0], port, parts[0] == "tcp" || parts[0] == "udp"
}
func bytesValue(v int64) string {
	if v%(1024*1024) == 0 {
		return strconv.FormatInt(v/(1024*1024), 10) + "MiB"
	}
	return strconv.FormatInt(v, 10) + "B"
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/seatbelt"
	"github.com/inizio/nexus/packages/nexus/pkg/server"
)

var firecrackerProbeGOOS = goruntime.GOOS

var firecrackerProbeOutputFn = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type CommandRunner struct{}

type backendAliasDriver struct {
	alias string
	inner runtime.Driver
}

type backendAliasConnectorDriver struct {
	backendAliasDriver
}

func (d *backendAliasConnectorDriver) AgentConn(ctx context.Context, workspaceID string) (net.Conn, error) {
	if connector, ok := d.inner.(interface {
		AgentConn(context.Context, string) (net.Conn, error)
	}); ok {
		return connector.AgentConn(ctx, workspaceID)
	}
	return nil, fmt.Errorf("%s runtime does not support agent connection", d.alias)
}

func (d *backendAliasDriver) Backend() string { return d.alias }

func (d *backendAliasDriver) Create(ctx context.Context, req runtime.CreateRequest) error {
	return d.inner.Create(ctx, req)
}

func (d *backendAliasDriver) Start(ctx context.Context, workspaceID string) error {
	return d.inner.Start(ctx, workspaceID)
}

func (d *backendAliasDriver) Stop(ctx context.Context, workspaceID string) error {
	return d.inner.Stop(ctx, workspaceID)
}

func (d *backendAliasDriver) Restore(ctx context.Context, workspaceID string) error {
	return d.inner.Restore(ctx, workspaceID)
}

func (d *backendAliasDriver) Pause(ctx context.Context, workspaceID string) error {
	return d.inner.Pause(ctx, workspaceID)
}

func (d *backendAliasDriver) Resume(ctx context.Context, workspaceID string) error {
	return d.inner.Resume(ctx, workspaceID)
}

func (d *backendAliasDriver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	return d.inner.Fork(ctx, workspaceID, childWorkspaceID)
}

func (d *backendAliasDriver) Destroy(ctx context.Context, workspaceID string) error {
	return d.inner.Destroy(ctx, workspaceID)
}

func (r *CommandRunner) Run(ctx context.Context, dir string, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = dir
	return c.Run()
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	defaultWorkspaceDir := resolveDefaultWorkspaceDir()
	workspaceDir := flag.String("workspace-dir", defaultWorkspaceDir, "Workspace directory path")
	token := flag.String("token", "", "JWT secret token for authentication")
	flag.Parse()

	if *token == "" {
		log.Fatal("Error: --token is required")
	}

	if err := runServer(*port, *workspaceDir, *token); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func resolveDefaultWorkspaceDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "nexus", "workspaces")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/workspace"
	}
	return filepath.Join(home, ".local", "state", "nexus", "workspaces")
}

func runServer(port int, workspaceDir string, token string) error {
	srv, err := server.NewServer(port, workspaceDir, token)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	runner := &CommandRunner{}

	// Create firecracker manager with default config
	fcManager := firecracker.NewManager(firecracker.ManagerConfig{
		FirecrackerBin: "firecracker",
		KernelPath:     os.Getenv("NEXUS_FIRECRACKER_KERNEL"),
		RootFSPath:     os.Getenv("NEXUS_FIRECRACKER_ROOTFS"),
		WorkDirRoot:    filepath.Join(workspaceDir, "firecracker-vms"),
	})

	// Create runtime drivers.
	firecrackerDriver := firecracker.NewDriver(runner, firecracker.WithManager(fcManager))
	seatbeltDriver := seatbelt.NewDriver()

	firecrackerAvailable := probeFirecrackerTooling(exec.LookPath)
	seatbeltAvailable := firecrackerProbeGOOS == "darwin"

	_, codexErr := exec.LookPath("codex")
	codexAvailable := codexErr == nil

	_, opencodeErr := exec.LookPath("opencode")
	opencodeAvailable := opencodeErr == nil

	capabilities := []runtime.Capability{
		{Name: "runtime.firecracker", Available: firecrackerAvailable},
		{Name: "runtime.seatbelt", Available: seatbeltAvailable},
		{Name: "runtime.linux", Available: firecrackerAvailable},
		{Name: "spotlight.tunnel", Available: true},
		{Name: "auth.profile.git", Available: true},
		{Name: "auth.profile.codex", Available: codexAvailable},
		{Name: "auth.profile.opencode", Available: opencodeAvailable},
	}

	drivers := map[string]runtime.Driver{
		"firecracker": firecrackerDriver,
		"seatbelt":    seatbeltDriver,
	}

	factory := runtime.NewFactory(capabilities, drivers)
	srv.SetRuntimeFactory(factory)

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		srv.Shutdown()
	}()

	log.Printf("Workspace daemon started on port %d", port)
	return srv.Start()
}

// probeFirecrackerTooling checks if native firecracker binary is available
func probeFirecrackerTooling(lookPath func(string) (string, error)) bool {
	if firecrackerProbeGOOS == "darwin" && probeLimaFirecrackerInstanceReady(lookPath) {
		return true
	}
	if _, err := lookPath("firecracker"); err != nil {
		return false
	}
	if !nestedVirtualizationSupported() {
		return false
	}
	return true
}

func probeLimaFirecrackerInstanceReady(lookPath func(string) (string, error)) bool {
	if _, err := lookPath("limactl"); err != nil {
		return false
	}

	out, err := firecrackerProbeOutputFn("limactl", "list", "--json", "nexus-firecracker")
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" {
		return false
	}

	type limaInstance struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	var entries []limaInstance
	if err := json.Unmarshal([]byte(trimmed), &entries); err == nil {
		for _, entry := range entries {
			if strings.TrimSpace(entry.Name) == "nexus-firecracker" && strings.EqualFold(strings.TrimSpace(entry.Status), "running") {
				return true
			}
		}
		return false
	}

	var single limaInstance
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil {
		return strings.TrimSpace(single.Name) == "nexus-firecracker" && strings.EqualFold(strings.TrimSpace(single.Status), "running")
	}

	return false
}

func nestedVirtualizationSupported() bool {
	if goruntime.GOOS == "linux" {
		if info, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			s := strings.ToLower(string(info))
			return strings.Contains(s, " vmx") || strings.Contains(s, " svm")
		}
		return false
	}
	if goruntime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "kern.hv_support").Output()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) == "1"
	}
	return false
}

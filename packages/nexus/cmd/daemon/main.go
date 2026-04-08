package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/local"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/lxc"
	"github.com/inizio/nexus/packages/nexus/pkg/server"
)

type CommandRunner struct{}

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

	// Create firecracker driver with manager using the new constructor pattern
	firecrackerDriver := firecracker.NewDriver(runner, firecracker.WithManager(fcManager))
	lxcDriver := lxc.NewDriver(firecrackerDriver)
	localDriver := local.NewDriver()

	firecrackerAvailable := probeFirecrackerTooling(exec.LookPath)
	lxcAvailable := probeLXCOrLimaTooling(exec.LookPath)

	_, codexErr := exec.LookPath("codex")
	codexAvailable := codexErr == nil

	_, opencodeErr := exec.LookPath("opencode")
	opencodeAvailable := opencodeErr == nil

	capabilities := []runtime.Capability{
		{Name: "runtime.firecracker", Available: firecrackerAvailable},
		{Name: "runtime.lxc", Available: lxcAvailable},
		{Name: "runtime.local", Available: true},
		{Name: "runtime.linux", Available: firecrackerAvailable || lxcAvailable},
		{Name: "spotlight.tunnel", Available: true},
		{Name: "auth.profile.git", Available: true},
		{Name: "auth.profile.codex", Available: codexAvailable},
		{Name: "auth.profile.opencode", Available: opencodeAvailable},
	}

	drivers := map[string]runtime.Driver{
		"firecracker": firecrackerDriver,
		"lxc":         lxcDriver,
		"local":       localDriver,
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
	if _, err := lookPath("firecracker"); err != nil {
		return false
	}
	if !nestedVirtualizationSupported() {
		return false
	}
	return true
}

func probeLXCOrLimaTooling(lookPath func(string) (string, error)) bool {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "linux" {
		return false
	}
	if _, err := lookPath("limactl"); err == nil {
		if _, lxcErr := lookPath("lxc"); lxcErr == nil {
			return true
		}
		if _, lxdErr := lookPath("lxd"); lxdErr == nil {
			return true
		}
		return true
	}
	if _, err := lookPath("lxc"); err == nil {
		return true
	}
	if _, err := lookPath("lxd"); err == nil {
		return true
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

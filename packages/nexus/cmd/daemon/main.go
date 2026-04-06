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
	"syscall"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/local"
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
	workspaceDir := flag.String("workspace-dir", "/workspace", "Workspace directory path")
	token := flag.String("token", "", "JWT secret token for authentication")
	flag.Parse()

	if *token == "" {
		log.Fatal("Error: --token is required")
	}

	if err := runServer(*port, *workspaceDir, *token); err != nil {
		log.Fatalf("Server error: %v", err)
	}
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
	localDriver := local.NewDriver()

	firecrackerAvailable := probeFirecrackerTooling(exec.LookPath)

	_, codexErr := exec.LookPath("codex")
	codexAvailable := codexErr == nil

	_, opencodeErr := exec.LookPath("opencode")
	opencodeAvailable := opencodeErr == nil

	capabilities := []runtime.Capability{
		{Name: "runtime.firecracker", Available: firecrackerAvailable},
		{Name: "runtime.local", Available: true},
		{Name: "spotlight.tunnel", Available: true},
		{Name: "auth.profile.git", Available: true},
		{Name: "auth.profile.codex", Available: codexAvailable},
		{Name: "auth.profile.opencode", Available: opencodeAvailable},
	}

	drivers := map[string]runtime.Driver{
		"firecracker": firecrackerDriver,
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
	_, err := lookPath("firecracker")
	return err == nil
}

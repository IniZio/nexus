package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/auth"
	"github.com/inizio/nexus/packages/nexus/pkg/daemonclient"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/limafirecracker"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/local"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/seatbelt"
	"github.com/inizio/nexus/packages/nexus/pkg/server"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
)

var firecrackerProbeGOOS = goruntime.GOOS

var firecrackerProbeOutputFn = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type CommandRunner struct{}

func (r *CommandRunner) Run(ctx context.Context, dir string, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = dir
	return c.Run()
}

func main() {
	port := flag.Int("port", 63987, "Port to listen on")
	defaultWorkspaceDir := resolveDefaultWorkspaceDir()
	workspaceDir := flag.String("workspace-dir", defaultWorkspaceDir, "Workspace directory path")
	tokenFlag := flag.String("token", "", "JWT secret (optional; if unset, a token is loaded or created under --data-dir)")
	defaultDataDir, err := daemonclient.DefaultDataDir()
	if err != nil {
		log.Fatalf("Error: data directory: %v", err)
	}
	dataDir := flag.String("data-dir", defaultDataDir, "Daemon data directory (stores token file)")
	flag.Parse()

	token := strings.TrimSpace(*tokenFlag)
	if token == "" {
		var tokErr error
		token, tokErr = loadOrCreateToken(*dataDir)
		if tokErr != nil {
			log.Fatalf("Error: %v", tokErr)
		}
	}

	if err := runServer(*port, *workspaceDir, token); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

func loadOrCreateToken(dataDir string) (string, error) {
	tokenPath := filepath.Join(dataDir, "token")

	if data, err := os.ReadFile(tokenPath); err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
	}

	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return token, nil
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
	_ = runtime.MaybeAutoinstallPreflightHostTools()
	applyDaemonFirecrackerAssetDefaults()

	srv, err := server.NewServer(port, workspaceDir, token)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}
	srv.SetAuthProvider(auth.NewLocalTokenProvider(token))

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

	firecrackerRuntimeDriver := runtime.Driver(firecrackerDriver)
	if firecrackerProbeGOOS == "darwin" {
		// On macOS, the firecracker backend alias is implemented through the
		// Lima-based runtime driver.
		firecrackerRuntimeDriver = limafirecracker.NewDriver(seatbeltDriver)
	}

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
		"firecracker": firecrackerRuntimeDriver,
		"seatbelt":    seatbeltDriver,
		"local":       local.NewDriver(),
	}

	factory := runtime.NewFactory(capabilities, drivers)
	srv.SetRuntimeFactory(factory)

	// Initialize live port monitoring
	agentConnFn := seatbeltDriver.AgentConn
	if connector, ok := firecrackerRuntimeDriver.(interface {
		AgentConn(context.Context, string) (net.Conn, error)
	}); ok {
		agentConnFn = connector.AgentConn
	}
	portScanner := spotlight.NewShellPortScanner(agentConnFn)
	portMonitor := spotlight.NewPortMonitor(srv.SpotlightManager(), portScanner, 5*time.Second)
	srv.SetPortMonitor(portMonitor)

	// Resume port monitoring and re-apply compose ports for workspaces that
	// were already running when the daemon (re)started.
	srv.ResumeRunningWorkspaces(context.Background())
	srv.StartPTYMaintenance(context.Background(), 2*time.Minute)

	liveIDs := map[string]struct{}{}
	for _, id := range srv.WorkspaceIDs() {
		liveIDs[id] = struct{}{}
	}
	if err := fcManager.ReconcileOrphans(context.Background(), liveIDs); err != nil {
		log.Printf("firecracker reconcile: %v", err)
	}

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		srv.Shutdown()
	}()

	log.Printf("Workspace daemon started on port %d", port)
	return srv.Start()
}

func applyDaemonFirecrackerAssetDefaults() {
	const defK = "/var/lib/nexus/vmlinux.bin"
	const defR = "/var/lib/nexus/rootfs.ext4"
	if strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_KERNEL")) == "" {
		if st, err := os.Stat(defK); err == nil && !st.IsDir() {
			_ = os.Setenv("NEXUS_FIRECRACKER_KERNEL", defK)
		}
	}
	if strings.TrimSpace(os.Getenv("NEXUS_FIRECRACKER_ROOTFS")) == "" {
		if st, err := os.Stat(defR); err == nil && !st.IsDir() {
			_ = os.Setenv("NEXUS_FIRECRACKER_ROOTFS", defR)
		}
	}
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

	out, err := firecrackerProbeOutputFn("limactl", "list", "--json", "nexus")
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
			if strings.TrimSpace(entry.Name) == "nexus" && strings.EqualFold(strings.TrimSpace(entry.Status), "running") {
				return true
			}
		}
		return false
	}

	var single limaInstance
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil {
		return strings.TrimSpace(single.Name) == "nexus" && strings.EqualFold(strings.TrimSpace(single.Status), "running")
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

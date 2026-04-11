package firecracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/mdlayher/vsock"
)

var _ runtime.Driver = (*Driver)(nil)

type CommandRunner interface {
	Run(ctx context.Context, dir string, cmd string, args ...string) error
}

// ManagerInterface defines the interface for VM lifecycle management
type ManagerInterface interface {
	Spawn(ctx context.Context, spec SpawnSpec) (*Instance, error)
	Stop(ctx context.Context, workspaceID string) error
	Get(workspaceID string) (*Instance, error)
	GrowWorkspace(ctx context.Context, workspaceID string, newSizeBytes int64) error
}

type Driver struct {
	runner       CommandRunner
	manager      ManagerInterface
	projectRoots map[string]string
	agents       map[string]*AgentClient
	mu           sync.RWMutex
}


func (d *Driver) AgentConn(ctx context.Context, workspaceID string) (net.Conn, error) {
	if d.manager == nil {
		return nil, errors.New("manager is required for firecracker driver")
	}

	inst, err := d.manager.Get(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace instance lookup failed: %w", err)
	}

	if inst == nil || inst.CID == 0 {
		return nil, errors.New("workspace instance has no guest CID")
	}

	conn, err := vsock.Dial(inst.CID, DefaultAgentVSockPort, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial failed: %w", err)
	}

	return conn, nil
}

type Option func(*Driver)

func WithManager(manager ManagerInterface) Option {
	return func(d *Driver) {
		d.manager = manager
	}
}

func NewDriver(runner CommandRunner, opts ...Option) *Driver {
	d := &Driver{
		runner:       runner,
		projectRoots: make(map[string]string),
		agents:       make(map[string]*AgentClient),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *Driver) Backend() string {
	return "firecracker"
}

func (d *Driver) workspaceDir(workspaceID string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if dir, ok := d.projectRoots[workspaceID]; ok {
		return dir
	}
	return ""
}

func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
	if req.ProjectRoot == "" {
		return errors.New("project root is required")
	}

	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	memMiB := 1024
	if req.Options != nil {
		if memStr, ok := req.Options["mem_mib"]; ok && memStr != "" {
			if val, err := strconv.Atoi(memStr); err == nil {
				memMiB = val
			}
		}
	}

	spec := SpawnSpec{
		WorkspaceID: req.WorkspaceID,
		ProjectRoot: req.ProjectRoot,
		MemoryMiB:   memMiB,
		VCPUs:       1,
	}

	inst, err := d.manager.Spawn(ctx, spec)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.projectRoots[req.WorkspaceID] = req.ProjectRoot
	d.mu.Unlock()

	if shouldBootstrapGuestTooling(req.Options) {
		if err := d.bootstrapGuestToolingAndAuth(ctx, req.WorkspaceID, req.ConfigBundle); err != nil {
			return err
		}
	}

	// TODO: Connect to agent via vsock when AgentClient dial is implemented
	_ = inst

	return nil
}

func shouldBootstrapGuestTooling(options map[string]string) bool {
	if options == nil {
		return false
	}
	raw := strings.TrimSpace(options["host_cli_sync"])
	if raw == "" {
		return false
	}
	raw = strings.ToLower(raw)
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func (d *Driver) bootstrapGuestToolingAndAuth(ctx context.Context, workspaceID string, authBundle string) error {
	if strings.TrimSpace(authBundle) == "" {
		return nil
	}

	conn, err := d.waitForAgentConn(ctx, workspaceID, 30*time.Second)
	if err != nil {
		return fmt.Errorf("bootstrap firecracker guest agent connection failed: %w", err)
	}
	defer conn.Close()

	client := NewAgentClient(conn)
	env := []string{}
	if strings.TrimSpace(authBundle) != "" {
		env = append(env, "NEXUS_HOST_AUTH_BUNDLE="+authBundle)
	}

	request := ExecRequest{
		ID:      fmt.Sprintf("bootstrap-%d", time.Now().UnixNano()),
		Command: "sh",
		Args:    []string{"-lc", buildGuestCLIBootstrapCommand()},
		WorkDir: "/workspace",
		Env:     env,
	}
	result, execErr := client.Exec(ctx, request)
	if execErr != nil {
		return fmt.Errorf("bootstrap firecracker guest tooling failed: %w", execErr)
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("bootstrap firecracker guest tooling failed: %s", detail)
	}

	return nil
}

func (d *Driver) waitForAgentConn(ctx context.Context, workspaceID string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := d.AgentConn(ctx, workspaceID)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("agent connection timed out")
}

func detectHostCLIAvailability() map[string]bool {
	out := make(map[string]bool)
	for _, bin := range agentprofile.AllBinaries() {
		_, err := exec.LookPath(bin)
		out[bin] = err == nil
	}
	return out
}

func buildGuestCLIBootstrapCommand() string {
	parts := []string{
		"set -e",
		"mkdir -p ~/.config ~/.local/share",
		`if [ -n "${NEXUS_HOST_AUTH_BUNDLE:-}" ]; then ` +
			`(printf '%s' "$NEXUS_HOST_AUTH_BUNDLE" | base64 -d 2>/dev/null || printf '%s' "$NEXUS_HOST_AUTH_BUNDLE" | base64 -D 2>/dev/null) >/tmp/nexus-auth.tar.gz && ` +
			`tar -xzf /tmp/nexus-auth.tar.gz -C "$HOME" >/dev/null 2>&1 || true; ` +
			`rm -f /tmp/nexus-auth.tar.gz >/dev/null 2>&1 || true; fi`,
		`if command -v npm >/dev/null 2>&1; then NPM_BIN=$(npm bin -g 2>/dev/null || true); if [ -n "$NPM_BIN" ] && [ -d "$NPM_BIN" ]; then export PATH="$NPM_BIN:$PATH"; fi; fi`,
	}

	pkgs := agentprofile.AllInstallPkgs()
	if len(pkgs) > 0 {
		joined := strings.Join(pkgs, " ")
		parts = append(parts, "if command -v npm >/dev/null 2>&1; then npm i -g "+joined+" >/dev/null 2>&1 || true; fi")
	}

	for _, bin := range agentprofile.AllBinaries() {
		parts = append(parts, "command -v "+bin+" >/dev/null 2>&1")
	}

	return strings.Join(parts, "; ")
}

func (d *Driver) GrowWorkspace(ctx context.Context, workspaceID string, newSizeBytes int64) error {
	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	if err := d.manager.GrowWorkspace(ctx, workspaceID, newSizeBytes); err != nil {
		return fmt.Errorf("host-side grow failed: %w", err)
	}

	conn, err := d.AgentConn(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("agent connect for disk.grow: %w", err)
	}
	defer conn.Close()

	client := NewAgentClient(conn)
	req := ExecRequest{
		ID:      fmt.Sprintf("disk-grow-%d", time.Now().UnixNano()),
		Type:    "disk.grow",
		Command: "",
	}
	result, err := client.Exec(ctx, req)
	if err != nil {
		return fmt.Errorf("disk.grow exec failed: %w", err)
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("resize2fs failed: %s", detail)
	}

	return nil
}

func (d *Driver) Start(ctx context.Context, workspaceID string) error {
	// Native firecracker VMs start immediately after Spawn
	// This is a no-op for the native implementation
	return nil
}

func (d *Driver) Stop(ctx context.Context, workspaceID string) error {
	if d.manager == nil {
		return errors.New("manager is required for firecracker driver")
	}

	d.mu.Lock()
	delete(d.agents, workspaceID)
	d.mu.Unlock()

	return d.manager.Stop(ctx, workspaceID)
}

func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	// Native firecracker doesn't support restore in this cutover
	return errors.New("restore not supported in native firecracker driver")
}

func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	// Native firecracker doesn't support pause in this cutover
	return errors.New("pause not supported in native firecracker driver")
}

func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	// Native firecracker doesn't support resume in this cutover
	return errors.New("resume not supported in native firecracker driver")
}

func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	// Native firecracker doesn't support fork in this cutover
	return errors.New("fork not supported in native firecracker driver")
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	delete(d.projectRoots, workspaceID)
	delete(d.agents, workspaceID)
	d.mu.Unlock()

	// Stop the VM if manager is available
	if d.manager != nil {
		// Ignore error - workspace may already be stopped
		_ = d.manager.Stop(ctx, workspaceID)
	}

	return nil
}

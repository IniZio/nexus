package lxc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/drivers/shared"
)

var lxcLimaInstanceBase = []string{"nexus-lxc", "nexus-firecracker", "default"}

type Driver struct {
	mu          sync.RWMutex
	workspaces  map[string]*workspaceState
	spawnShell  func(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error)
	instanceEnv string

	bootstrapMu          sync.Mutex
	bootstrappedInstance map[string]bool
	bootstrapInstance    func(ctx context.Context, instance string) error
}

type workspaceState struct {
	projectRoot string
	state       string
	instance    string
}

func NewDriver(_ runtime.Driver) *Driver {
	return &Driver{
		workspaces:           make(map[string]*workspaceState),
		spawnShell:           startLimaShell,
		instanceEnv:          strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_LXC_INSTANCE")),
		bootstrappedInstance: make(map[string]bool),
		bootstrapInstance:    bootstrapLimaDockerTooling,
	}
}

func (d *Driver) Backend() string { return "lxc" }

func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
	_ = ctx
	if req.WorkspaceID == "" {
		return fmt.Errorf("workspace id is required")
	}

	d.mu.Lock()

	if _, exists := d.workspaces[req.WorkspaceID]; exists {
		d.mu.Unlock()
		return fmt.Errorf("workspace %s already exists", req.WorkspaceID)
	}

	instance := d.instanceNameForOptions(req.Options)
	d.workspaces[req.WorkspaceID] = &workspaceState{projectRoot: req.ProjectRoot, state: "created", instance: instance}
	d.mu.Unlock()

	if err := d.ensureInstanceBootstrapped(ctx, instance); err != nil {
		d.mu.Lock()
		delete(d.workspaces, req.WorkspaceID)
		d.mu.Unlock()
		return err
	}

	d.mu.Lock()
	d.mu.Unlock()
	return nil
}

func (d *Driver) Start(ctx context.Context, workspaceID string) error {
	_ = ctx
	return d.setState(workspaceID, "running")
}

func (d *Driver) Stop(ctx context.Context, workspaceID string) error {
	_ = ctx
	return d.setState(workspaceID, "stopped")
}

func (d *Driver) Restore(ctx context.Context, workspaceID string) error {
	_ = ctx
	return d.setState(workspaceID, "running")
}

func (d *Driver) Pause(ctx context.Context, workspaceID string) error {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	if ws.state == "running" {
		ws.state = "paused"
	}
	return nil
}

func (d *Driver) Resume(ctx context.Context, workspaceID string) error {
	_ = ctx
	return d.setState(workspaceID, "running")
}

func (d *Driver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()

	parent, ok := d.workspaces[workspaceID]
	if !ok {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	if _, exists := d.workspaces[childWorkspaceID]; exists {
		return fmt.Errorf("workspace %s already exists", childWorkspaceID)
	}

	d.workspaces[childWorkspaceID] = &workspaceState{projectRoot: parent.projectRoot, state: "created"}
	return nil
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.workspaces[workspaceID]; !ok {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	delete(d.workspaces, workspaceID)
	return nil
}

func (d *Driver) setState(workspaceID, state string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		ws = &workspaceState{state: state, instance: d.defaultInstanceName()}
		d.workspaces[workspaceID] = ws
		return nil
	}
	ws.state = state
	return nil
}

func (d *Driver) AgentConn(ctx context.Context, workspaceID string) (net.Conn, error) {
	_ = ctx
	left, right := net.Pipe()
	go d.serveShellProtocol(context.Background(), workspaceID, right)
	return left, nil
}

func (d *Driver) serveShellProtocol(ctx context.Context, workspaceID string, conn net.Conn) {
	defer conn.Close()

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	var writeMu sync.Mutex
	writeJSON := func(msg map[string]any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(msg)
	}

	type shellSession struct {
		id   string
		cmd  *exec.Cmd
		ptmx *os.File
	}

	var session *shellSession
	closeSession := func() {
		if session == nil {
			return
		}
		_ = session.ptmx.Close()
		if session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
			_, _ = session.cmd.Process.Wait()
		}
		session = nil
	}

	for {
		var req map[string]any
		if err := dec.Decode(&req); err != nil {
			closeSession()
			return
		}

		typ, _ := req["type"].(string)
		id, _ := req["id"].(string)

		switch typ {
		case "shell.open":
			closeSession()
			shell, _ := req["command"].(string)
			if strings.TrimSpace(shell) == "" {
				shell = "bash"
			}
			workdir, _ := req["workdir"].(string)
			localPath := ""
			if strings.TrimSpace(workdir) == "" || strings.TrimSpace(workdir) == "/workspace" {
				localPath = d.workspaceProjectRoot(workspaceID)
				workdir = "/workspace"
			}

			instance := d.workspaceInstance(workspaceID)
			cmd, ptmx, err := d.spawnShell(ctx, instance, workdir, localPath, shell)
			if err != nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": err.Error()})
				continue
			}

			d.mu.Lock()
			if ws, ok := d.workspaces[workspaceID]; ok {
				ws.state = "running"
				// Prefer localPath (macOS host path) for projectRoot so that
				// workspaceProjectRoot always returns the real host path.
				storeRoot := localPath
				if storeRoot == "" {
					storeRoot = workdir
				}
				if strings.TrimSpace(storeRoot) != "" {
					ws.projectRoot = storeRoot
				}
				if strings.TrimSpace(instance) != "" {
					ws.instance = instance
				}
			}
			d.mu.Unlock()

			session = &shellSession{id: id, cmd: cmd, ptmx: ptmx}
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 0})

			go func(s *shellSession) {
				buf := make([]byte, 4096)
				for {
					n, err := s.ptmx.Read(buf)
					if n == 0 && err == nil {
						continue
					}
					if n > 0 {
						clean := shared.SanitizeLimaShellChunk(string(buf[:n]))
						if clean != "" {
							_ = writeJSON(map[string]any{"id": s.id, "type": "chunk", "stream": "stdout", "data": clean})
						}
					}
					if err != nil {
						break
					}
				}

				exitCode := 0
				if s.cmd.Process != nil {
					_, _ = s.cmd.Process.Wait()
				}
				if s.cmd.ProcessState != nil {
					exitCode = s.cmd.ProcessState.ExitCode()
				}
				_ = writeJSON(map[string]any{"id": s.id, "type": "result", "exit_code": exitCode})
				d.mu.Lock()
				if ws, ok := d.workspaces[workspaceID]; ok {
					ws.state = "stopped"
				}
				d.mu.Unlock()
			}(session)

		case "shell.write":
			if session == nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": "no active shell session"})
				continue
			}
			data, _ := req["data"].(string)
			if _, err := session.ptmx.Write([]byte(data)); err != nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": err.Error()})
				continue
			}
			_ = writeJSON(map[string]any{"id": id, "type": "ack", "ok": true})

		case "shell.resize":
			if session == nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": "no active shell session"})
				continue
			}
			cols := toInt(req["cols"], 120)
			rows := toInt(req["rows"], 30)
			if err := pty.Setsize(session.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": err.Error()})
				continue
			}
			_ = writeJSON(map[string]any{"id": id, "type": "ack", "ok": true})

		case "shell.close":
			closeSession()
			_ = writeJSON(map[string]any{"id": id, "type": "ack", "ok": true})
			return

		default:
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": fmt.Sprintf("unknown request type %q", typ)})
		}
	}
}

func startLimaShell(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error) {
	launchShell := strings.TrimSpace(shell)
	if launchShell == "" {
		launchShell = "bash"
	}

	workdir = strings.TrimSpace(workdir)
	localPath = strings.TrimSpace(localPath)

	candidates := shared.InstanceCandidates(instanceName, lxcLimaInstanceBase)
	if discovered, err := shared.ListLimaInstances(ctx); err == nil && len(discovered) > 0 {
		fmt.Fprintf(os.Stderr, "[nexus-lxc] discovered instances=%v requested=%q candidates(before)=%v\n", discovered, instanceName, candidates)
		candidates = shared.FilterCandidatesSortedFallback(candidates, discovered)
		fmt.Fprintf(os.Stderr, "[nexus-lxc] candidates(after)=%v\n", candidates)
	}

	// If a localPath is provided and workdir is /workspace, bind-mount the
	// worktree into /workspace inside the Lima VM before opening the PTY.
	// localPath is the macOS host path that virtiofs exposes inside the VM.
	if localPath != "" && workdir == "/workspace" {
		for _, candidate := range candidates {
			setupArgs := []string{"shell", candidate, "--",
				"sudo", "bash", "-c",
				fmt.Sprintf("mkdir -p /workspace && mount --bind %q /workspace", localPath),
			}
			setupCmd := exec.CommandContext(ctx, "limactl", setupArgs...)
			if out, err := setupCmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "[nexus-lxc] bind-mount setup failed instance=%s err=%v out=%s\n", candidate, err, string(out))
			} else {
				fmt.Fprintf(os.Stderr, "[nexus-lxc] bind-mount /workspace -> %s in instance=%s\n", localPath, candidate)
				break
			}
		}
	}
	var lastErr error
	for _, candidate := range candidates {
		fmt.Fprintf(os.Stderr, "[nexus-lxc] trying instance=%s workdir=%q shell=%q\n", candidate, workdir, launchShell)
		// Note: do NOT pass --shell to limactl. When --shell is set, limactl
		// passes the shell path as a trailing SSH argument which becomes a
		// "--" prefix flag (e.g. "-- /bin/bash"), causing bash to exit with
		// "bash: --: invalid option". Lima defaults to the instance's login
		// shell (bash), so omitting --shell is correct and safe.
		args := []string{"shell", "--reconnect", candidate}
		// Note: do NOT pass --workdir to limactl. When --workdir is set,
		// limactl injects "-- /bin/bash" as the SSH remote command which causes
		// bash to fail with "--: invalid option". Instead, we cd after the PTY
		// starts by writing to stdin.
		// If a non-default shell is explicitly requested, run it as a command
		// via the default shell to avoid the --shell flag issue.
		if launchShell != "bash" && launchShell != "/bin/bash" {
			args = append(args, "--", launchShell)
		}
		cmd := exec.CommandContext(ctx, "limactl", args...)
		if ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 120}); err == nil {
			fmt.Fprintf(os.Stderr, "[nexus-lxc] started shell with instance=%s\n", candidate)
			// Set the working directory by writing to the PTY. We use a goroutine
			// with a delay to allow bash to finish initialization (read .bashrc,
			// print the first prompt) before we inject the cd command. 500ms is
			// conservative; the shell prompt normally appears within ~300ms.
			if workdir != "" {
				go func() {
					time.Sleep(500 * time.Millisecond)
					_, _ = fmt.Fprintf(ptmx, "cd %s 2>/dev/null; clear\n", shared.ShellQuote(workdir))
				}()
			}
			return cmd, ptmx, nil
		} else {
			lastErr = err
			fmt.Fprintf(os.Stderr, "[nexus-lxc] start failed instance=%s err=%v\n", candidate, err)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no lima instance candidates available")
	}
	return nil, nil, fmt.Errorf("lima shell start failed: %w", lastErr)
}

func toInt(value any, fallback int) int {
	switch v := value.(type) {
	case float64:
		if int(v) > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return fallback
}

func (d *Driver) workspaceProjectRoot(workspaceID string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if ws, ok := d.workspaces[workspaceID]; ok {
		return ws.projectRoot
	}
	return ""
}

func (d *Driver) workspaceInstance(workspaceID string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if ws, ok := d.workspaces[workspaceID]; ok && strings.TrimSpace(ws.instance) != "" {
		return ws.instance
	}
	return d.defaultInstanceName()
}

func (d *Driver) defaultInstanceName() string {
	if strings.TrimSpace(d.instanceEnv) != "" {
		return strings.TrimSpace(d.instanceEnv)
	}
	if fromDoctor := strings.TrimSpace(os.Getenv("NEXUS_DOCTOR_LXC_INSTANCE")); fromDoctor != "" {
		return fromDoctor
	}
	return "nexus-lxc"
}

func (d *Driver) ensureInstanceBootstrapped(ctx context.Context, instance string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = d.defaultInstanceName()
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_LXC_BOOTSTRAP_DOCKER")), "0") {
		return nil
	}

	d.bootstrapMu.Lock()
	defer d.bootstrapMu.Unlock()
	if d.bootstrappedInstance[instance] {
		return nil
	}
	if d.bootstrapInstance == nil {
		d.bootstrappedInstance[instance] = true
		return nil
	}
	if err := d.bootstrapInstance(ctx, instance); err != nil {
		return err
	}
	d.bootstrappedInstance[instance] = true
	return nil
}

func bootstrapLimaDockerTooling(ctx context.Context, instance string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "nexus-firecracker"
	}
	candidates := shared.InstanceCandidates(instance, lxcLimaInstanceBase)
	if discovered, err := shared.ListLimaInstances(ctx); err == nil && len(discovered) > 0 {
		candidates = shared.FilterCandidatesSortedFallback(candidates, discovered)
	}

	script := strings.Join([]string{
		"set -e",
		"if command -v docker >/dev/null 2>&1 && (docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1) && (docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1); then exit 0; fi",
		"sudo -n apt-get update",
		"sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 || sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose",
		"sudo -n systemctl enable docker >/dev/null 2>&1 || true",
		"sudo -n systemctl start docker >/dev/null 2>&1 || sudo -n service docker start >/dev/null 2>&1 || true",
		"sudo -n usermod -aG docker \"$USER\" >/dev/null 2>&1 || true",
		"(docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1)",
		"(docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1)",
	}, "; ")

	var lastErr error
	for _, candidate := range candidates {
		out, err := shared.LimactlShellScript(ctx, candidate, script)
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("bootstrap docker tooling in %s failed: %s", candidate, strings.TrimSpace(string(out)))
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("bootstrap docker tooling failed: no lima instance candidates")
}

func (d *Driver) instanceNameForOptions(opts map[string]string) string {
	if opts != nil {
		if v := strings.TrimSpace(opts["lima.instance"]); v != "" {
			return v
		}
	}
	return d.defaultInstanceName()
}


package seatbelt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/drivers/shared"
)

var seatbeltLookPath = exec.LookPath
var ensureLimaInstanceRunningFn = ensureLimaInstanceRunning
var prepareWorkspacePathFn = prepareWorkspacePath
var teardownWorkspacePathFn = teardownWorkspacePath
var listLimaInstancesFn = shared.ListLimaInstances
var ptyStartWithSizeFn = pty.StartWithSize
var limactlOutputFn = shared.DefaultLimactlOutput
var limactlCombinedOutputFn = shared.DefaultLimactlCombinedOutput

var seatbeltLimaInstanceBase = []string{"nexus-seatbelt", "nexus-firecracker", "default"}

type Driver struct {
	mu                 sync.RWMutex
	workspaces         map[string]*workspaceState
	spawnShell         func(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error)
	instanceEnv        string
	bootstrapGuard     *shared.BootstrapOnceGuard
	bootstrapInstance  func(ctx context.Context, instance, configBundle string) error
	prepareWorkspaceFS func(ctx context.Context, instance, targetPath, localPath string) error
}

type workspaceState struct {
	projectRoot string
	state       string
	instance    string
}

func NewDriver() *Driver {
	return &Driver{
		workspaces:         make(map[string]*workspaceState),
		spawnShell:         startLimaShell,
		instanceEnv:        strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_SEATBELT_INSTANCE")),
		bootstrapGuard:     shared.NewBootstrapOnceGuard(),
		bootstrapInstance:  bootstrapSeatbeltTooling,
		prepareWorkspaceFS: prepareWorkspacePath,
	}
}

func (d *Driver) Backend() string { return "seatbelt" }

func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
	if strings.TrimSpace(req.WorkspaceID) == "" {
		return fmt.Errorf("workspace id is required")
	}
	if strings.TrimSpace(req.ProjectRoot) == "" {
		return fmt.Errorf("project root is required")
	}
	if _, err := os.Stat(req.ProjectRoot); err != nil {
		return fmt.Errorf("project root not accessible: %w", err)
	}
	if _, err := seatbeltLookPath("limactl"); err != nil {
		return fmt.Errorf("seatbelt runtime requires limactl for isolated guest")
	}

	instance := d.instanceNameForOptions(req.Options)

	d.mu.Lock()
	if existing, exists := d.workspaces[req.WorkspaceID]; exists {
		if strings.TrimSpace(existing.projectRoot) != "" {
			d.mu.Unlock()
			return nil
		}
		existing.projectRoot = req.ProjectRoot
		existing.instance = instance
		d.mu.Unlock()
		return nil
	}
	d.workspaces[req.WorkspaceID] = &workspaceState{projectRoot: req.ProjectRoot, state: "created", instance: instance}
	d.mu.Unlock()

	if err := d.ensureInstanceBootstrapped(ctx, instance, req.ConfigBundle); err != nil {
		d.mu.Lock()
		delete(d.workspaces, req.WorkspaceID)
		d.mu.Unlock()
		return err
	}

	if d.prepareWorkspaceFS != nil {
		targetPath := guestWorkdirForID(req.WorkspaceID)
		if err := d.prepareWorkspaceFS(ctx, instance, targetPath, req.ProjectRoot); err != nil {
			if strings.TrimSpace(instance) == "nexus-seatbelt" {
				fallbackCandidates := []string{"nexus-firecracker", "mvm", "default"}
				for _, fallback := range fallbackCandidates {
					if fallbackErr := d.prepareWorkspaceFS(ctx, fallback, targetPath, req.ProjectRoot); fallbackErr != nil {
						continue
					}
					if ws, ok := d.workspaces[req.WorkspaceID]; ok {
						ws.instance = fallback
					}
					return nil
				}
			}
			d.mu.Lock()
			delete(d.workspaces, req.WorkspaceID)
			d.mu.Unlock()
			return fmt.Errorf("%w: %v", runtime.ErrWorkspaceMountFailed, err)
		}
	}

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
	return d.setState(workspaceID, "paused")
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

	d.workspaces[childWorkspaceID] = &workspaceState{projectRoot: parent.projectRoot, state: "created", instance: parent.instance}
	return nil
}

func (d *Driver) Destroy(ctx context.Context, workspaceID string) error {
	d.mu.Lock()
	ws, ok := d.workspaces[workspaceID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("workspace %s not found", workspaceID)
	}
	instance := ws.instance
	if strings.TrimSpace(instance) == "" {
		instance = d.defaultInstanceName()
	}
	delete(d.workspaces, workspaceID)
	d.mu.Unlock()

	_ = teardownWorkspacePathFn(ctx, instance, workspaceID)
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
			perWsPath := guestWorkdirForID(workspaceID)
			localPath := ""
			if strings.TrimSpace(workdir) == "" || strings.TrimSpace(workdir) == "/workspace" || strings.TrimSpace(workdir) == perWsPath {
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
				if strings.TrimSpace(localPath) != "" {
					ws.projectRoot = localPath
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

		case "ports.scan":
			ports := d.scanPorts(ctx, workspaceID)
			_ = writeJSON(map[string]any{
				"id":   id,
				"type": "ports.result",
				"ports": ports,
			})

		default:
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": fmt.Sprintf("unknown request type %q", typ)})
		}
	}
}

func startLimaShell(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error) {
	launchShell := shared.NormalizeLaunchShell(shell)
	workdir = strings.TrimSpace(workdir)
	localPath = strings.TrimSpace(localPath)

	candidates := shared.InstanceCandidates(instanceName, seatbeltLimaInstanceBase)
	if discovered, err := listLimaInstancesFn(ctx); err == nil && len(discovered) > 0 {
		candidates = shared.ApplyLimaDiscovery(candidates, discovered, true)
	}

	if localPath != "" {
		mounted := false
		var lastMountErr string
		for _, candidate := range candidates {
			if err := ensureLimaInstanceRunningFn(ctx, candidate); err != nil {
				lastMountErr = err.Error()
				continue
			}
			prepErr := prepareWorkspacePathFn(ctx, candidate, workdir, localPath)
			if prepErr == nil {
				candidates = []string{candidate}
				mounted = true
				break
			}
			lastMountErr = prepErr.Error()
		}
		if !mounted {
			if strings.TrimSpace(lastMountErr) == "" {
				lastMountErr = "no available lima candidates"
			}
			return nil, nil, fmt.Errorf("prepare workspace mount failed: %s", lastMountErr)
		}
	}

	return shared.TryLimactlShellPTY(ctx, shared.TryLimactlPTYOptions{
		Candidates:          candidates,
		LaunchShell:         launchShell,
		Workdir:             workdir,
		BeforeEachCandidate: ensureLimaInstanceRunningFn,
		PtyStart:            ptyStartWithSizeFn,
		ErrPrefix:           "seatbelt lima shell start failed",
	})
}

func guestWorkdirForID(workspaceID string) string {
	return "/nexus/ws/" + workspaceID
}

func (d *Driver) GuestWorkdir(workspaceID string) string {
	_ = workspaceID
	return "/workspace"
}

func prepareWorkspacePath(ctx context.Context, instance, targetPath, localPath string) error {
	if strings.TrimSpace(instance) == "" {
		return fmt.Errorf("instance is required")
	}
	if strings.TrimSpace(localPath) == "" {
		return fmt.Errorf("workspace path is required")
	}
	if strings.TrimSpace(targetPath) == "" {
		return fmt.Errorf("target path is required")
	}

	script := fmt.Sprintf(
		"set -e; MNTPT=%s; if mountpoint -q \"$MNTPT\" 2>/dev/null; then sudo umount \"$MNTPT\"; fi; sudo mkdir -p \"$MNTPT\"; sudo mount --bind %s \"$MNTPT\"",
		shared.ShellQuote(targetPath),
		shared.ShellQuote(localPath),
	)
	out, err := shared.LimactlShellScript(ctx, instance, script)
	if err != nil {
		return fmt.Errorf("prepare workspace path %s -> %s failed: %s", localPath, targetPath, strings.TrimSpace(string(out)))
	}
	return nil
}

func teardownWorkspacePath(ctx context.Context, instance, workspaceID string) error {
	if strings.TrimSpace(instance) == "" || strings.TrimSpace(workspaceID) == "" {
		return nil
	}
	targetPath := guestWorkdirForID(workspaceID)
	script := fmt.Sprintf(
		"MNTPT=%s; if mountpoint -q \"$MNTPT\" 2>/dev/null; then sudo umount -l \"$MNTPT\" 2>/dev/null || sudo umount \"$MNTPT\" 2>/dev/null || true; fi; sudo rmdir \"$MNTPT\" 2>/dev/null || true",
		shared.ShellQuote(targetPath),
	)
	out, err := shared.LimactlShellScript(ctx, instance, script)
	if err != nil {
		return fmt.Errorf("teardown workspace path for %s failed: %s", workspaceID, strings.TrimSpace(string(out)))
	}
	return nil
}

func bootstrapSeatbeltTooling(ctx context.Context, instance, configBundle string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "nexus-firecracker"
	}

	candidates := shared.InstanceCandidates(instance, seatbeltLimaInstanceBase)
	if discovered, err := listLimaInstancesFn(ctx); err == nil && len(discovered) > 0 {
		candidates = shared.ApplyLimaDiscovery(candidates, discovered, true)
	}

	script := buildSeatbeltBootstrapScript(configBundle)
	return shared.RunLimactlBootstrapScript(ctx, candidates, script, shared.LimactlBootstrapOptions{
		EnsureBeforeCandidate:   ensureLimaInstanceRunningFn,
		MaxAttemptsPerCandidate: 3,
		RetryDelay:              500 * time.Millisecond,
		RetryIf:                 shared.IsTransientLimaShellError,
		ErrNoCandidates:         "bootstrap seatbelt tooling failed: no lima instance candidates",
		FormatFailure: func(candidate, trimmed string) error {
			return fmt.Errorf("bootstrap seatbelt tooling in %s failed: %s", candidate, trimmed)
		},
	})
}

func ensureLimaInstanceRunning(ctx context.Context, instance string) error {
	return shared.EnsureLimaInstanceRunning(ctx, instance, limactlOutputFn, limactlCombinedOutputFn)
}

func buildSeatbeltBootstrapScript(configBundle string) string {
	parts := []string{
		"set -e",
		buildCredentialSymlinkCleanup(),
	}

	if strings.TrimSpace(configBundle) != "" {
		quotedBundle := shared.ShellQuote(configBundle)
		parts = append(parts,
			`printf '%s' `+quotedBundle+` >/tmp/nexus-auth.tar.gz.b64`,
			`(cat /tmp/nexus-auth.tar.gz.b64 | base64 -d 2>/dev/null || cat /tmp/nexus-auth.tar.gz.b64 | base64 -D 2>/dev/null) >/tmp/nexus-auth.tar.gz`,
			`tar -xzf /tmp/nexus-auth.tar.gz -C "$HOME" >/dev/null 2>&1 || true`,
			`rm -f /tmp/nexus-auth.tar.gz.b64 /tmp/nexus-auth.tar.gz >/dev/null 2>&1 || true`,
		)
	}

	parts = append(parts,
		"unset DOCKER_HOST DOCKER_CONTEXT",
		"if ! (command -v docker >/dev/null 2>&1 && (docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1) && (docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1) && command -v make >/dev/null 2>&1); then sudo -n apt-get update; sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 make curl ca-certificates gnupg nodejs npm || sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose make curl ca-certificates gnupg nodejs npm; sudo -n systemctl enable docker >/dev/null 2>&1 || true; sudo -n systemctl start docker >/dev/null 2>&1 || sudo -n service docker start >/dev/null 2>&1 || true; sudo -n usermod -aG docker $USER >/dev/null 2>&1 || true; fi",
		"(docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1)",
		"(docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1)",
		"command -v make >/dev/null 2>&1",
	)

	pkgs := agentprofile.AllInstallPkgs()
	if len(pkgs) > 0 {
		joined := strings.Join(pkgs, " ")
		parts = append(parts,
			"if command -v npm >/dev/null 2>&1; then cd /tmp >/dev/null 2>&1 || true; npm i -g "+joined+" >/dev/null 2>&1 || sudo -n npm i -g "+joined+" >/dev/null 2>&1 || true; fi",
		)
	}

	for _, bin := range agentprofile.AllBinaries() {
		parts = append(parts, "if command -v "+bin+" >/dev/null 2>&1; then "+bin+" --version >/dev/null 2>&1 || true; fi")
	}

	parts = append(parts,
		"mkdir -p ~/.config ~/.local/share",
		"if command -v npm >/dev/null 2>&1; then cd /tmp >/dev/null 2>&1 || true; NPM_BIN=$(npm bin -g 2>/dev/null || true); if [ -n \"$NPM_BIN\" ] && [ -d \"$NPM_BIN\" ]; then export PATH=\"$NPM_BIN:$PATH\"; fi; fi",
	)
	return strings.Join(parts, "; ")
}

func buildCredentialSymlinkCleanup() string {
	dirs := make(map[string]struct{})
	files := make(map[string]struct{})
	for _, cf := range agentprofile.AllCredFiles() {
		dir := filepath.Dir(cf)
		dirs[dir] = struct{}{}
		files[cf] = struct{}{}
	}
	var checks []string
	for dir := range dirs {
		checks = append(checks, `if [ -L "$HOME/`+dir+`" ]; then rm -f "$HOME/`+dir+`"; fi`)
	}
	for file := range files {
		checks = append(checks, `if [ -L "$HOME/`+file+`" ]; then rm -f "$HOME/`+file+`"; fi`)
	}
	return strings.Join(checks, "; ")
}

func (d *Driver) instanceNameForOptions(opts map[string]string) string {
	if opts != nil {
		if v := strings.TrimSpace(opts["lima.instance"]); v != "" {
			return v
		}
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
	return "nexus-seatbelt"
}

func (d *Driver) ensureInstanceBootstrapped(ctx context.Context, instance, configBundle string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = d.defaultInstanceName()
	}
	return d.bootstrapGuard.Ensure(instance, func() error {
		if d.bootstrapInstance == nil {
			return nil
		}
		return d.bootstrapInstance(ctx, instance, configBundle)
	})
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

func (d *Driver) scanPorts(ctx context.Context, workspaceID string) []map[string]any {
	instance := d.workspaceInstance(workspaceID)

	// Use ss to list listening TCP ports
	script := `ss -tlnp 2>/dev/null | awk 'NR>1 {split($4, a, ":"); print a[length(a)], $NF}' | while read port process; do
		if [ -n "$port" ] && [ "$port" != "0" ] && [ -n "$process" ]; then
			echo "{\"port\": $port, \"process\": \"$process\"}"
		fi
	done`

	out, err := shared.LimactlShellScript(ctx, instance, script)
	if err != nil {
		return nil
	}

	var ports []map[string]any
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}

		var portInfo struct {
			Port    int    `json:"port"`
			Process string `json:"process"`
		}
		if err := json.Unmarshal([]byte(line), &portInfo); err != nil {
			continue
		}
		if portInfo.Port > 0 {
			port := map[string]any{
				"address": fmt.Sprintf("0.0.0.0:%d", portInfo.Port),
				"port":    portInfo.Port,
				"process": portInfo.Process,
			}
			ports = append(ports, port)
		}
	}
	return ports
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

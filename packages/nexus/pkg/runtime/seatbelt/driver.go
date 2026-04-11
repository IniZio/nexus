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
)

var seatbeltLookPath = exec.LookPath
var ensureLimaInstanceRunningFn = ensureLimaInstanceRunning
var prepareWorkspaceMountFn = prepareWorkspaceMount
var listLimaInstancesFn = listLimaInstances
var ptyStartWithSizeFn = pty.StartWithSize
var limactlOutputFn = defaultLimactlOutput
var limactlCombinedOutputFn = defaultLimactlCombinedOutput

func defaultLimactlOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "limactl", args...)
	return cmd.Output()
}

func defaultLimactlCombinedOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "limactl", args...)
	return cmd.CombinedOutput()
}

type Driver struct {
	mu                 sync.RWMutex
	workspaces         map[string]*workspaceState
	spawnShell         func(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error)
	instanceEnv        string
	hostHome           string
	bootstrapMu        sync.Mutex
	bootstrapped       map[string]bool
	bootstrapInstance  func(ctx context.Context, instance, hostHome, configBundle string) error
	prepareWorkspaceFS func(ctx context.Context, instance, localPath string) error
}

type workspaceState struct {
	projectRoot string
	state       string
	instance    string
}

func NewDriver() *Driver {
	homeDir, _ := os.UserHomeDir()
	return &Driver{
		workspaces:         make(map[string]*workspaceState),
		spawnShell:         startLimaShell,
		instanceEnv:        strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_SEATBELT_INSTANCE")),
		hostHome:           strings.TrimSpace(homeDir),
		bootstrapped:       make(map[string]bool),
		bootstrapInstance:  bootstrapSeatbeltTooling,
		prepareWorkspaceFS: prepareWorkspaceMount,
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
	if _, exists := d.workspaces[req.WorkspaceID]; exists {
		d.mu.Unlock()
		return fmt.Errorf("workspace %s already exists", req.WorkspaceID)
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
		if err := d.prepareWorkspaceFS(ctx, instance, req.ProjectRoot); err != nil {
			if strings.TrimSpace(instance) == "nexus-seatbelt" {
				fallbackCandidates := []string{"nexus-firecracker", "mvm", "default"}
				for _, fallback := range fallbackCandidates {
					if fallbackErr := d.prepareWorkspaceFS(ctx, fallback, req.ProjectRoot); fallbackErr != nil {
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
			overrideLocalPath, _ := req["local_path"].(string)
			localPath := ""
			if strings.TrimSpace(workdir) == "" || strings.TrimSpace(workdir) == "/workspace" {
				if strings.TrimSpace(overrideLocalPath) != "" {
					localPath = strings.TrimSpace(overrideLocalPath)
				} else {
					localPath = d.workspaceProjectRoot(workspaceID)
				}
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
						clean := sanitizeLimaShellChunk(string(buf[:n]))
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

	candidates := instanceCandidates(instanceName)
	if discovered, err := listLimaInstancesFn(ctx); err == nil && len(discovered) > 0 {
		candidates = filterCandidatesByAvailability(candidates, discovered)
	}

	if localPath != "" && workdir == "/workspace" {
		mounted := false
		for _, candidate := range candidates {
			if err := ensureLimaInstanceRunningFn(ctx, candidate); err != nil {
				continue
			}
			if err := prepareWorkspaceMountFn(ctx, candidate, localPath); err == nil {
				instanceName = candidate
				candidates = []string{candidate}
				mounted = true
				break
			}
		}
		if !mounted {
			workdir = localPath
		}
	}

	var lastErr error
	for _, candidate := range candidates {
		if err := ensureLimaInstanceRunningFn(ctx, candidate); err != nil {
			lastErr = err
			continue
		}
		args := []string{"shell", "--reconnect", candidate}
		if launchShell != "bash" && launchShell != "/bin/bash" {
			args = append(args, "--", launchShell)
		}
		cmd := exec.CommandContext(ctx, "limactl", args...)
		if ptmx, err := ptyStartWithSizeFn(cmd, &pty.Winsize{Rows: 30, Cols: 120}); err == nil {
			if workdir != "" {
				go func() {
					time.Sleep(500 * time.Millisecond)
					_, _ = fmt.Fprintf(ptmx, "cd %s 2>/dev/null; clear\n", shellQuote(workdir))
				}()
			}
			return cmd, ptmx, nil
		} else {
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no lima instance candidates available")
	}
	return nil, nil, fmt.Errorf("seatbelt lima shell start failed: %w", lastErr)
}

func prepareWorkspaceMount(ctx context.Context, instance, localPath string) error {
	if strings.TrimSpace(instance) == "" {
		return fmt.Errorf("instance is required")
	}
	if strings.TrimSpace(localPath) == "" {
		return fmt.Errorf("workspace path is required")
	}

	script := fmt.Sprintf("set -e; sudo rm -rf /workspace; sudo ln -sfn %s /workspace; cd /workspace >/dev/null 2>&1", shellQuote(localPath))
	cmd := exec.CommandContext(ctx, "limactl", "shell", instance, "--", "sh", "-lc", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("prepare /workspace mount failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func bootstrapSeatbeltTooling(ctx context.Context, instance, hostHome, configBundle string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "nexus-firecracker"
	}

	candidates := instanceCandidates(instance)
	if discovered, err := listLimaInstancesFn(ctx); err == nil && len(discovered) > 0 {
		candidates = filterCandidatesByAvailability(candidates, discovered)
	}

	script := buildSeatbeltBootstrapScript(hostHome, configBundle)

	var lastErr error
	for _, candidate := range candidates {
		if err := ensureLimaInstanceRunningFn(ctx, candidate); err != nil {
			lastErr = err
			continue
		}

		const maxAttempts = 3
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			cmd := exec.CommandContext(ctx, "limactl", "shell", candidate, "--", "sh", "-lc", script)
			out, err := cmd.CombinedOutput()
			if err == nil {
				return nil
			}

			trimmed := strings.TrimSpace(string(out))
			lastErr = fmt.Errorf("bootstrap seatbelt tooling in %s failed: %s", candidate, trimmed)
			if !isTransientLimaShellError(trimmed) || attempt == maxAttempts {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("bootstrap seatbelt tooling failed: no lima instance candidates")
}

func isTransientLimaShellError(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"kex_exchange_identification",
		"connection reset by peer",
		"connection closed by remote host",
		"broken pipe",
		"mux_client_request_session",
		"session open refused by peer",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func ensureLimaInstanceRunning(ctx context.Context, instance string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return fmt.Errorf("instance is required")
	}

	out, err := limactlOutputFn(ctx, "list", "--json", instance)
	if err != nil {
		if startOut, startErr := limactlCombinedOutputFn(ctx, "start", "--yes", "--name", instance, "template:default"); startErr != nil {
			return fmt.Errorf(
				"lima list failed for %s: %w; lima start failed for %s: %s",
				instance, err, instance, strings.TrimSpace(string(startOut)),
			)
		}
		return nil
	}
	trimmed := strings.TrimSpace(string(out))

	if trimmed == "" || trimmed == "[]" {
		if startOut, startErr := limactlCombinedOutputFn(ctx, "start", "--yes", "--name", instance, "template:default"); startErr != nil {
			return fmt.Errorf("lima start failed for %s: %s", instance, strings.TrimSpace(string(startOut)))
		}
		return nil
	}

	if strings.Contains(trimmed, `"status":"Running"`) {
		return nil
	}

	if strings.Contains(trimmed, `"status":"Stopped"`) {
		if startOut, startErr := limactlCombinedOutputFn(ctx, "start", "--yes", instance); startErr != nil {
			return fmt.Errorf("lima start failed for %s: %s", instance, strings.TrimSpace(string(startOut)))
		}
		return nil
	}

	return nil
}

func buildSeatbeltBootstrapScript(hostHome, configBundle string) string {
	parts := []string{
		"set -e",
		buildCredentialSymlinkCleanup(),
	}

	if strings.TrimSpace(configBundle) != "" {
		parts = append(parts,
			`export NEXUS_HOST_AUTH_BUNDLE='`+configBundle+`'`,
			`if [ -n "${NEXUS_HOST_AUTH_BUNDLE:-}" ]; then `+
				`mkdir -p "$HOME"; `+
				`(printf '%s' "$NEXUS_HOST_AUTH_BUNDLE" | base64 -d 2>/dev/null || printf '%s' "$NEXUS_HOST_AUTH_BUNDLE" | base64 -D 2>/dev/null) >/tmp/nexus-auth.tar.gz && `+
				`tar -xzf /tmp/nexus-auth.tar.gz -C "$HOME" >/dev/null 2>&1 || true; `+
				`rm -f /tmp/nexus-auth.tar.gz >/dev/null 2>&1 || true; fi`,
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

	hostHome = strings.TrimSpace(hostHome)
	if hostHome != "" {
		parts = append(parts,
			"mkdir -p ~/.config ~/.local/share",
			"if command -v npm >/dev/null 2>&1; then cd /tmp >/dev/null 2>&1 || true; NPM_BIN=$(npm bin -g 2>/dev/null || true); if [ -n \"$NPM_BIN\" ] && [ -d \"$NPM_BIN\" ]; then export PATH=\"$NPM_BIN:$PATH\"; fi; fi",
		)
		parts = append(parts, buildCredentialSymlinks(hostHome))
	}

	return strings.Join(parts, "; ")
}

func buildCredentialSymlinkCleanup() string {
	dirs := make(map[string]struct{})
	for _, cf := range agentprofile.AllCredFiles() {
		dir := filepath.Dir(cf)
		dirs[dir] = struct{}{}
	}
	var checks []string
	for dir := range dirs {
		checks = append(checks, `if [ -L "$HOME/`+dir+`" ]; then rm -f "$HOME/`+dir+`"; fi`)
	}
	return strings.Join(checks, "; ")
}

func buildCredentialSymlinks(hostHome string) string {
	var parts []string
	for _, cf := range agentprofile.AllCredFiles() {
		dir := filepath.Dir(cf)
		hostPath := shellQuote(filepath.Join(hostHome, cf))
		parts = append(parts,
			`mkdir -p "$HOME/`+dir+`"`,
			`if [ -e `+hostPath+` ]; then ln -sfn `+hostPath+` "$HOME/`+cf+`"; fi`,
		)
	}
	return strings.Join(parts, "; ")
}

func listLimaInstances(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "limactl", "ls", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(out), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func filterCandidatesByAvailability(candidates []string, available []string) []string {
	if len(candidates) == 0 || len(available) == 0 {
		return candidates
	}

	availableSet := make(map[string]struct{}, len(available))
	for _, name := range available {
		availableSet[strings.TrimSpace(name)] = struct{}{}
	}

	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := availableSet[candidate]; ok {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return candidates
}

func instanceCandidates(instanceName string) []string {
	trimmed := strings.TrimSpace(instanceName)
	base := []string{"nexus-seatbelt", "nexus-firecracker", "default"}
	if trimmed == "" {
		return base
	}
	out := make([]string, 0, len(base)+1)
	out = append(out, trimmed)
	for _, candidate := range base {
		if candidate == trimmed {
			continue
		}
		out = append(out, candidate)
	}
	return out
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

	d.bootstrapMu.Lock()
	defer d.bootstrapMu.Unlock()
	if d.bootstrapped[instance] {
		return nil
	}
	if d.bootstrapInstance == nil {
		d.bootstrapped[instance] = true
		return nil
	}
	if err := d.bootstrapInstance(ctx, instance, d.hostHome, configBundle); err != nil {
		return err
	}
	d.bootstrapped[instance] = true
	return nil
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

func sanitizeLimaShellChunk(chunk string) string {
	trimmed := strings.TrimSpace(chunk)
	if trimmed == "" {
		return chunk
	}
	for _, noise := range []string{
		"mux_client_request_session: session request failed: Session open refused by peer",
		"ux_client_request_session: session request failed: Session open refused by peer",
		"ControlSocket ",
		"already exists, disconnecting",
		"disabling multiplexing",
		"Exiting ssh session for the instance",
	} {
		if strings.Contains(trimmed, noise) {
			return ""
		}
	}
	return chunk
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

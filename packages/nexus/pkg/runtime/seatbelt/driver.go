package seatbelt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

var seatbeltLimaInstanceBase = []string{"nexus"}

const workspaceMarkerFile = ".nexus-workspace-marker.json"

type Driver struct {
	mu                 sync.RWMutex
	workspaces         map[string]*workspaceState
	snapshotRoot       string
	spawnShell         func(ctx context.Context, instanceName, workdir, localPath, shell string) (*exec.Cmd, *os.File, error)
	instanceEnv        string
	bootstrapGuard     *shared.BootstrapOnceGuard
	bootstrapInstance  func(ctx context.Context, instance, configBundle string) error
	applyConfigBundle  func(ctx context.Context, instance, configBundle string) error
	prepareWorkspaceFS func(ctx context.Context, instance, targetPath, localPath string) error
}

var _ runtime.ForkSnapshotter = (*Driver)(nil)

type workspaceState struct {
	projectRoot string
	state       string
	instance    string
}

func NewDriver() *Driver {
	return &Driver{
		workspaces:         make(map[string]*workspaceState),
		snapshotRoot:       defaultSeatbeltSnapshotRoot(),
		spawnShell:         startLimaShell,
		instanceEnv:        strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_SEATBELT_INSTANCE")),
		bootstrapGuard:     shared.NewBootstrapOnceGuard(),
		bootstrapInstance:  bootstrapSeatbeltTooling,
		applyConfigBundle:  applySeatbeltConfigBundle,
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
	if snapshotID := strings.TrimSpace(req.Options["lineage_snapshot_id"]); snapshotID != "" {
		if err := d.restoreLineageSnapshot(snapshotID, req.ProjectRoot); err != nil {
			return fmt.Errorf("restore lineage snapshot %s: %w", snapshotID, err)
		}
	}

	instance := d.instanceNameForOptions(req.Options)

	d.mu.Lock()
	if existing, exists := d.workspaces[req.WorkspaceID]; exists {
		existing.projectRoot = req.ProjectRoot
		existing.instance = instance
		d.mu.Unlock()
		if err := d.ensureInstanceBootstrapped(ctx, instance, ""); err != nil {
			return err
		}
		if d.prepareWorkspaceFS != nil {
			targetPath := guestWorkdirForID(req.WorkspaceID)
			if err := d.prepareWorkspaceOnCandidates(ctx, req.WorkspaceID, instance, targetPath, req.ProjectRoot); err != nil {
				return fmt.Errorf("%w: %v", runtime.ErrWorkspaceMountFailed, err)
			}
		}
		return nil
	}
	d.workspaces[req.WorkspaceID] = &workspaceState{projectRoot: req.ProjectRoot, state: "created", instance: instance}
	d.mu.Unlock()

	if err := d.ensureInstanceBootstrapped(ctx, instance, ""); err != nil {
		d.mu.Lock()
		delete(d.workspaces, req.WorkspaceID)
		d.mu.Unlock()
		return err
	}
	if d.applyConfigBundle != nil {
		if err := d.applyConfigBundle(ctx, instance, req.ConfigBundle); err != nil {
			d.mu.Lock()
			delete(d.workspaces, req.WorkspaceID)
			d.mu.Unlock()
			return err
		}
	}

	if d.prepareWorkspaceFS != nil {
		targetPath := guestWorkdirForID(req.WorkspaceID)
		if err := d.prepareWorkspaceOnCandidates(ctx, req.WorkspaceID, instance, targetPath, req.ProjectRoot); err != nil {
			d.mu.Lock()
			delete(d.workspaces, req.WorkspaceID)
			d.mu.Unlock()
			return fmt.Errorf("%w: %v", runtime.ErrWorkspaceMountFailed, err)
		}
	}

	return nil
}

func (d *Driver) CheckpointFork(ctx context.Context, workspaceID, childWorkspaceID string) (string, error) {
	_ = ctx
	sourceRoot := strings.TrimSpace(d.workspaceProjectRoot(workspaceID))
	if sourceRoot == "" {
		return "", fmt.Errorf("workspace %s not found", workspaceID)
	}
	if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
		return "", fmt.Errorf("source workspace path unavailable: %s", sourceRoot)
	}

	snapshotID := fmt.Sprintf("lima-fc-%s-%s-%d",
		strings.TrimSpace(workspaceID),
		strings.TrimSpace(childWorkspaceID),
		time.Now().UTC().UnixNano(),
	)
	snapshotPath := d.snapshotPath(snapshotID)
	if err := os.RemoveAll(snapshotPath); err != nil {
		return "", fmt.Errorf("reset snapshot path: %w", err)
	}
	if err := os.MkdirAll(snapshotPath, 0o755); err != nil {
		return "", fmt.Errorf("create snapshot path: %w", err)
	}
	if err := copyWorkspaceTree(sourceRoot, snapshotPath); err != nil {
		return "", fmt.Errorf("copy workspace snapshot: %w", err)
	}
	return snapshotID, nil
}

func (d *Driver) prepareWorkspaceOnCandidates(ctx context.Context, workspaceID, instance, targetPath, localPath string) error {
	if d.prepareWorkspaceFS == nil {
		return nil
	}
	if err := d.prepareWorkspaceFS(ctx, instance, targetPath, localPath); err == nil {
		return nil
	} else {
		// Try remaining candidates in the base list (handles legacy instance names).
		for _, fallback := range seatbeltLimaInstanceBase {
			if fallback == instance {
				continue
			}
			if fallbackErr := d.prepareWorkspaceFS(ctx, fallback, targetPath, localPath); fallbackErr != nil {
				continue
			}
			d.mu.Lock()
			if ws, ok := d.workspaces[workspaceID]; ok {
				ws.instance = fallback
			}
			d.mu.Unlock()
			return nil
		}
		return err
	}
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
				// Prefer the caller-supplied local_path (set by the PTY handler
				// from ws.LocalWorktreePath). Fall back to the in-memory
				// projectRoot which is only populated while the driver is alive.
				if v, _ := req["local_path"].(string); strings.TrimSpace(v) != "" {
					localPath = strings.TrimSpace(v)
				} else {
					localPath = d.workspaceProjectRoot(workspaceID)
				}
				workdir = perWsPath
			}

			instance := d.workspaceInstance(workspaceID)
			if err := d.ensureInstanceBootstrapped(ctx, instance, ""); err != nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": err.Error()})
				continue
			}
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
						_ = writeJSON(map[string]any{"id": s.id, "type": "chunk", "stream": "stdout", "data": string(buf[:n])})
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
				"id":    id,
				"type":  "ports.result",
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

	return shared.TrySSHShellPTY(ctx, shared.TrySSHPTYOptions{
		Candidates:          candidates,
		LaunchShell:         launchShell,
		Workdir:             workdir,
		BeforeEachCandidate: ensureLimaInstanceRunningFn,
		PtyStart:            ptyStartWithSizeFn,
		ErrPrefix:           "seatbelt lima shell start failed",
	})
}

func guestWorkdirForID(workspaceID string) string {
	_ = workspaceID
	return "/workspace"
}

func (d *Driver) GuestWorkdir(workspaceID string) string {
	return guestWorkdirForID(workspaceID)
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

	// Avoid remount churn when /workspace is already bound to the same source.
	// Repeated lazy unmount/remount cycles can invalidate cwd for long-running
	// tools (e.g. opencode), which then fail with "cwd was deleted".
	script := fmt.Sprintf(
		"set -e; MNTPT=%s; SRC=%s; sudo -n mkdir -p \"$MNTPT\"; CUR=$(findmnt -n -o SOURCE --target \"$MNTPT\" 2>/dev/null || true); if [ -n \"$CUR\" ]; then CUR_CANON=$(readlink -f \"$CUR\" 2>/dev/null || echo \"$CUR\"); SRC_CANON=$(readlink -f \"$SRC\" 2>/dev/null || echo \"$SRC\"); if [ \"$CUR_CANON\" = \"$SRC_CANON\" ]; then exit 0; fi; sudo -n umount -l \"$MNTPT\"; fi; sudo -n mount --bind \"$SRC\" \"$MNTPT\"",
		shared.ShellQuote(targetPath),
		shared.ShellQuote(localPath),
	)
	out, err := shared.DirectSSHScript(ctx, instance, script)
	if err != nil {
		log.Printf("[DEBUG scanPorts] Command error: %v", err)
		log.Printf("[DEBUG scanPorts] Raw output (on error): %s", string(out))
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
		"MNTPT=%s; if mountpoint -q \"$MNTPT\" 2>/dev/null; then sudo -n umount -l \"$MNTPT\" 2>/dev/null || sudo -n umount \"$MNTPT\" 2>/dev/null || true; fi; sudo -n rmdir \"$MNTPT\" 2>/dev/null || true",
		shared.ShellQuote(targetPath),
	)
	out, err := shared.DirectSSHScript(ctx, instance, script)
	if err != nil {
		log.Printf("[DEBUG scanPorts] Command error: %v", err)
		log.Printf("[DEBUG scanPorts] Raw output (on error): %s", string(out))
		return fmt.Errorf("teardown workspace path for %s failed: %s", workspaceID, strings.TrimSpace(string(out)))
	}
	return nil
}

func bootstrapSeatbeltTooling(ctx context.Context, instance, configBundle string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "nexus"
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
		`(sudo hostnamectl set-hostname nexus 2>/dev/null || (printf 'nexus\n' | sudo tee /etc/hostname >/dev/null 2>&1 && sudo hostname nexus 2>/dev/null)) || true`,
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
		"if ! (command -v docker >/dev/null 2>&1 && (docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1) && (docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1) && command -v make >/dev/null 2>&1); then sudo -n apt-get update; sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 make curl ca-certificates gnupg nodejs npm || sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose make curl ca-certificates gnupg nodejs npm; sudo -n systemctl enable docker >/dev/null 2>&1 || true; sudo -n systemctl start docker >/dev/null 2>&1 || sudo -n service docker start >/dev/null 2>&1 || true; fi",
		"sudo -n groupadd -f docker >/dev/null 2>&1 || true",
		"sudo -n usermod -aG docker $USER >/dev/null 2>&1 || true",
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

func applySeatbeltConfigBundle(ctx context.Context, instance, configBundle string) error {
	configBundle = strings.TrimSpace(configBundle)
	if configBundle == "" {
		return nil
	}
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "nexus"
	}
	candidates := shared.InstanceCandidates(instance, seatbeltLimaInstanceBase)
	if discovered, err := listLimaInstancesFn(ctx); err == nil && len(discovered) > 0 {
		candidates = shared.ApplyLimaDiscovery(candidates, discovered, true)
	}

	script := strings.Join([]string{
		"set -e",
		buildCredentialSymlinkCleanup(),
		"cat >/tmp/nexus-auth.tar.gz.b64",
		"(cat /tmp/nexus-auth.tar.gz.b64 | base64 -d 2>/dev/null || cat /tmp/nexus-auth.tar.gz.b64 | base64 -D 2>/dev/null) >/tmp/nexus-auth.tar.gz",
		"tar -xzf /tmp/nexus-auth.tar.gz -C \"$HOME\"",
		"rm -f /tmp/nexus-auth.tar.gz.b64 /tmp/nexus-auth.tar.gz >/dev/null 2>&1 || true",
	}, "; ")

	var lastErr error
	for _, candidate := range candidates {
		if err := ensureLimaInstanceRunningFn(ctx, candidate); err != nil {
			lastErr = err
			continue
		}
		out, err := shared.DirectSSHScriptWithInput(ctx, candidate, script, configBundle)
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("apply config bundle in %s failed: %s", candidate, strings.TrimSpace(string(out)))
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("apply config bundle failed: no lima instance candidates")
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
	return "nexus"
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

func (d *Driver) snapshotPath(snapshotID string) string {
	return filepath.Join(d.snapshotRoot, strings.TrimSpace(snapshotID))
}

func (d *Driver) restoreLineageSnapshot(snapshotID, targetPath string) error {
	snapshotPath := d.snapshotPath(snapshotID)
	info, err := os.Stat(snapshotPath)
	if err != nil {
		return fmt.Errorf("snapshot %s missing: %w", snapshotID, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("snapshot %s is invalid", snapshotID)
	}
	return copyWorkspaceTree(snapshotPath, targetPath)
}

func defaultSeatbeltSnapshotRoot() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Join(xdg, "nexus", "workspaces", "lineage-snapshots")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), "nexus", "lineage-snapshots")
	}
	return filepath.Join(home, ".local", "state", "nexus", "workspaces", "lineage-snapshots")
}

func copyWorkspaceTree(sourceRoot, targetRoot string) error {
	sourceRoot = strings.TrimSpace(sourceRoot)
	targetRoot = strings.TrimSpace(targetRoot)
	if sourceRoot == "" || targetRoot == "" {
		return fmt.Errorf("source and target paths are required")
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	return filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipWorkspacePath(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(targetRoot, rel)
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			return os.Symlink(linkTarget, target)
		case info.IsDir():
			return os.MkdirAll(target, mode.Perm())
		case mode.IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return copyFileWithMode(path, target, mode.Perm())
		default:
			return nil
		}
	})
}

func shouldSkipWorkspacePath(rel string) bool {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return false
	}
	if rel == workspaceMarkerFile {
		return true
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
		return true
	}
	if rel == ".worktrees" || strings.HasPrefix(rel, ".worktrees"+string(filepath.Separator)) {
		return true
	}
	return false
}

func copyFileWithMode(sourcePath, targetPath string, perm os.FileMode) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer targetFile.Close()
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return err
	}
	return targetFile.Chmod(perm)
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
	log.Printf("[DEBUG scanPorts] Using instance: %s", instance)
	// Use ss to list listening TCP ports
	script := `ss -tlnp 2>/dev/null | awk 'NR>1 {split($4, a, ":"); print a[length(a)], $NF}' | while read port process; do \
		if [ -n "$port" ] && [ "$port" != "0" ] && [ -n "$process" ]; then
			process_escaped=$(echo "$process" | sed 's/"/\\"/g')
			echo "{\"port\": $port, \"process\": \"$process_escaped\"}"
		fi
	done`

	log.Printf("[DEBUG scanPorts] Executing script: %s", script)
	out, err := shared.DirectSSHScript(ctx, instance, script)
	if err != nil {
		log.Printf("[DEBUG scanPorts] Command error: %v", err)
		log.Printf("[DEBUG scanPorts] Raw output (on error): %s", string(out))
		return nil
	}
	log.Printf("[DEBUG scanPorts] Raw output: %s", string(out))

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

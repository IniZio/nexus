package lxc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/creack/pty"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
)

var limaNoiseFragments = []string{
	"mux_client_request_session: session request failed: Session open refused by peer",
	"ControlSocket ",
	"already exists, disconnecting",
}

type Driver struct {
	mu          sync.RWMutex
	workspaces  map[string]*workspaceState
	spawnShell  func(ctx context.Context, instanceName, workdir, shell string) (*exec.Cmd, *os.File, error)
	instanceEnv string
}

type workspaceState struct {
	projectRoot string
	state       string
	instance    string
}

func NewDriver(_ runtime.Driver) *Driver {
	return &Driver{
		workspaces:  make(map[string]*workspaceState),
		spawnShell:  startLimaShell,
		instanceEnv: strings.TrimSpace(os.Getenv("NEXUS_RUNTIME_LXC_INSTANCE")),
	}
}

func (d *Driver) Backend() string { return "lxc" }

func (d *Driver) Create(ctx context.Context, req runtime.CreateRequest) error {
	_ = ctx
	if req.WorkspaceID == "" {
		return fmt.Errorf("workspace id is required")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workspaces[req.WorkspaceID]; exists {
		return fmt.Errorf("workspace %s already exists", req.WorkspaceID)
	}

	instance := d.instanceNameForOptions(req.Options)
	d.workspaces[req.WorkspaceID] = &workspaceState{projectRoot: req.ProjectRoot, state: "created", instance: instance}
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
			if strings.TrimSpace(workdir) == "" || strings.TrimSpace(workdir) == "/workspace" {
				workdir = d.workspaceProjectRoot(workspaceID)
			}

			instance := d.workspaceInstance(workspaceID)
			cmd, ptmx, err := d.spawnShell(ctx, instance, workdir, shell)
			if err != nil {
				_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": err.Error()})
				continue
			}

			d.mu.Lock()
			if ws, ok := d.workspaces[workspaceID]; ok {
				ws.state = "running"
				if strings.TrimSpace(workdir) != "" {
					ws.projectRoot = workdir
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
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 0})

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
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 0})

		case "shell.close":
			closeSession()
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 0})
			return

		default:
			_ = writeJSON(map[string]any{"id": id, "type": "result", "exit_code": 1, "stderr": fmt.Sprintf("unknown request type %q", typ)})
		}
	}
}

func startLimaShell(ctx context.Context, instanceName, workdir, shell string) (*exec.Cmd, *os.File, error) {
	if strings.TrimSpace(shell) == "" {
		shell = "bash"
	}

	cmdScript := ""
	if strings.TrimSpace(workdir) != "" {
		cmdScript += "cd " + shellQuote(workdir) + " 2>/dev/null || cd \"$HOME\"; "
	}
	launch := strings.TrimSpace(shell)
	switch launch {
	case "", "bash":
		launch = "bash -i"
	case "sh":
		launch = "sh -i"
	}
	cmdScript += "exec " + launch

	candidates := instanceCandidates(instanceName)
	var lastErr error
	for _, candidate := range candidates {
		cmd := exec.CommandContext(ctx, "limactl", "shell", candidate, "--", "sh", "-lc", cmdScript)
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 120})
		if err != nil {
			lastErr = err
			continue
		}
		return cmd, ptmx, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no lima instance candidates available")
	}
	return nil, nil, fmt.Errorf("lima shell start failed: %w", lastErr)
}

func instanceCandidates(instanceName string) []string {
	if strings.TrimSpace(instanceName) != "" {
		return []string{strings.TrimSpace(instanceName)}
	}
	return []string{"nexus-lxc", "nexus-firecracker"}
}

func shellQuote(v string) string {
	if strings.TrimSpace(v) == "" {
		return "''"
	}
	return strconv.Quote(v)
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

func (d *Driver) instanceNameForOptions(opts map[string]string) string {
	if opts != nil {
		if v := strings.TrimSpace(opts["lima.instance"]); v != "" {
			return v
		}
	}
	return d.defaultInstanceName()
}

func sanitizeLimaShellChunk(chunk string) string {
	trimmed := strings.TrimSpace(chunk)
	if trimmed == "" {
		return chunk
	}
	for _, noise := range limaNoiseFragments {
		if strings.Contains(trimmed, noise) {
			return ""
		}
	}
	return chunk
}

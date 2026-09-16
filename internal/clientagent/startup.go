package clientagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

// This file is the laptop side of auto port-forward. It must stay free of
// linux-only imports: the herdr startup hook runs on macOS, where the full
// nexus3 CLI does not build (cmd/nexus3-client is the darwin entry point).

type HerdrMachine struct {
	ProfileID string `json:"id"`
	SSHTarget string `json:"target"` // "user@host" or an ssh_config Host alias
	Enabled   bool   `json:"enabled"`
	Selected  bool   `json:"selected"`
	Session   string `json:"session"` // herdr session name; "" means default session
}

func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "nexus3", "portfwd-client")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "nexus3", "portfwd-client")
}

func ResolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", errors.New("herdr not found: HERDR_BIN_PATH is unset and no \"herdr\" binary is on PATH")
}

var ExecCommandContext = exec.CommandContext // tests swap it

func SanitizeSSHTarget(target string) string {
	var b strings.Builder
	for _, c := range target {
		switch c {
		case '@', '.', ':', '/', '-':
			b.WriteRune('_')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func RunStartup(ctx context.Context) error {
	stateDir := StateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("local-agent-startup: mkdir state: %w", err)
	}
	managers := make(map[string]*portfwd.Manager)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if err := Tick(ctx, stateDir, managers); err != nil {
			slog.Warn("local-agent-startup: tick", "err", err)
		}
	}
}

var RemoteStateReader = ReadRemoteForwardsState       // tests swap it
var ForwarderRunner portfwd.Runner = portfwd.OSRunner // tests swap it

// FocusResolverFunc resolves the sandbox ID for the focused workspace.
// Returns fallback=true on error (caller uses all rows); sandboxID="" with
// fallback=false means no focused workspace (desired is empty).
type FocusResolverFunc func(ctx context.Context, herdrBin, session, ctlPath, target string, runner portfwd.Runner) (sandboxID string, fallback bool)

var DefaultFocusResolver FocusResolverFunc = resolveFocusedSandboxID // tests swap it

var fallbackWarned sync.Map // key: "target\x00kind" → struct{}{}

func warnFallbackOnce(target, kind, msg string, args ...any) {
	if _, loaded := fallbackWarned.LoadOrStore(target+"\x00"+kind, struct{}{}); !loaded {
		slog.Warn(msg, args...)
	}
}

func clearFallback(target, kind string) {
	fallbackWarned.Delete(target + "\x00" + kind)
}

func filterToFocused(forwards []RemoteForwardEntry, sandboxID string, fallback bool) []RemoteForwardEntry {
	if fallback {
		return forwards
	}
	if sandboxID == "" {
		return nil
	}
	var out []RemoteForwardEntry
	for _, f := range forwards {
		// RemoteForwardEntry.Sandbox and forwards.state are both keyed by sandbox ID, not handle.
		if f.Sandbox == sandboxID {
			out = append(out, f)
		}
	}
	return out
}

func Tick(ctx context.Context, stateDir string, managers map[string]*portfwd.Manager) error {
	machines, err := DiscoverHerdrMachines(ctx)
	if err != nil {
		return fmt.Errorf("machine list: %w", err)
	}
	herdrBin, _ := ResolveHerdrBin()
	for _, m := range machines {
		if !m.Enabled || m.SSHTarget == "" {
			continue
		}
		if m.Session == "" {
			slog.Debug("portfwd focus: no session on machine, using default herdr session", "target", m.SSHTarget)
		}
		ctlPath := filepath.Join(stateDir, SanitizeSSHTarget(m.SSHTarget)+".ctl")
		fw := &portfwd.Forwarder{
			ControlPath: ctlPath,
			SSHHost:     m.SSHTarget,
			Run:         ForwarderRunner,
		}
		if err := fw.EnsureMaster(ctx); err != nil {
			slog.Warn("local-agent-startup: ensure-master", "target", m.SSHTarget, "err", err)
			continue
		}
		state, err := RemoteStateReader(ctx, ctlPath, m.SSHTarget)
		if err != nil {
			slog.Warn("local-agent-startup: read-remote-state", "target", m.SSHTarget, "err", err)
			continue
		}
		sandboxID, fallback := DefaultFocusResolver(ctx, herdrBin, m.Session, ctlPath, m.SSHTarget, fw.Run)
		focused := filterToFocused(state.Forwards, sandboxID, fallback)
		var desired []portfwd.Listener
		for _, fwd := range focused {
			if fwd.Status == "live" || fwd.Status == "pending" {
				desired = append(desired, portfwd.Listener{
					Port:    fwd.Port,
					Sandbox: portfwd.SandboxRef{ID: fwd.Sandbox, Status: portfwd.SandboxStatusRunning},
				})
			}
		}
		if _, ok := managers[m.SSHTarget]; !ok {
			managers[m.SSHTarget] = portfwd.NewManager(fw)
		}
		if err := managers[m.SSHTarget].Reconcile(ctx, desired); err != nil {
			slog.Warn("local-agent-startup: reconcile", "target", m.SSHTarget, "err", err)
		}
	}
	return nil
}

func DiscoverHerdrMachines(ctx context.Context) ([]HerdrMachine, error) {
	herdrBin, err := ResolveHerdrBin()
	if err != nil {
		return nil, fmt.Errorf("herdr not found: %w", err)
	}
	out, err := ExecCommandContext(ctx, herdrBin, "machine", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr machine list: %w", err)
	}
	var machines []HerdrMachine
	if err := json.Unmarshal(out, &machines); err != nil {
		return nil, fmt.Errorf("parse machine list: %w", err)
	}
	return machines, nil
}

type RemoteForwardEntry struct {
	Port    uint16 `json:"port"`
	Sandbox string `json:"sandbox"`
	Status  string `json:"status"`
}

type RemoteForwardsState struct {
	Forwards []RemoteForwardEntry `json:"forwards"`
}

// RemoteStateReadCommand returns the single remote command string.
// ONE argv element on purpose: ssh joins args with spaces, so a split
// {"sh","-c","cat <file>"} becomes `sh -c cat <file>` — cat reads stdin
// instead of the file (live bug 2026-09-15, engine-03 ↔ macOS herdr client).
func RemoteStateReadCommand() string {
	return "cat " + portfwd.RemoteStateFileShell() + " 2>/dev/null || echo '{\"forwards\":[]}'"
}

func ReadRemoteForwardsState(ctx context.Context, ctlPath, target string) (*RemoteForwardsState, error) {
	argv := ExecArgv(target, ctlPath, RemoteStateReadCommand())
	stdout, _, code, err := portfwd.OSRunner(ctx, argv)
	if err != nil {
		return nil, fmt.Errorf("ssh cat: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("ssh cat: exit %d", code)
	}
	var state RemoteForwardsState
	if err := json.Unmarshal([]byte(stdout), &state); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return &state, nil
}

func resolveFocusedSandboxID(ctx context.Context, herdrBin, session, ctlPath, target string, runner portfwd.Runner) (string, bool) {
	workspaceID, err := resolveFocusedWorkspaceID(ctx, herdrBin, session)
	if err != nil {
		warnFallbackOnce(target, "workspace", "portfwd focus: workspace list failed, using all rows", "target", target, "err", err)
		return "", true
	}
	clearFallback(target, "workspace")
	if workspaceID == "" {
		slog.Debug("portfwd focus: no focused workspace, no forwards", "target", target)
		return "", false
	}
	sandboxID, err := resolveRemoteSandboxIDForWorkspace(ctx, ctlPath, target, workspaceID, runner)
	if err != nil {
		warnFallbackOnce(target, "sandbox_id", "portfwd focus: remote sandbox_id lookup failed, using all rows", "target", target, "workspace_id", workspaceID, "err", err)
		return "", true
	}
	clearFallback(target, "sandbox_id")
	return sandboxID, false
}

func resolveFocusedWorkspaceID(ctx context.Context, herdrBin, session string) (string, error) {
	args := []string{"workspace", "list"}
	if session != "" {
		args = append([]string{"--session", session}, args...)
	}
	out, err := ExecCommandContext(ctx, herdrBin, args...).Output()
	if err != nil {
		return "", fmt.Errorf("herdr workspace list: %w", err)
	}
	return parseFocusedWorkspaceID(out)
}

func parseFocusedWorkspaceID(data []byte) (string, error) {
	var resp struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Focused     bool   `json:"focused"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parse workspace list: %w", err)
	}
	for _, ws := range resp.Result.Workspaces {
		if ws.Focused {
			return ws.WorkspaceID, nil
		}
	}
	return "", nil
}

// remoteNexus3HerdrListCmd returns the shell command to run `nexus3 herdr list`
// over non-interactive SSH. Tries $HOME/.local/bin/nexus3 first (absent from
// non-interactive PATH on Debian/Ubuntu), then falls back to the bare name.
func remoteNexus3HerdrListCmd() string {
	return `"$HOME/.local/bin/nexus3" herdr list 2>/dev/null || nexus3 herdr list`
}

func resolveRemoteSandboxIDForWorkspace(ctx context.Context, ctlPath, target, workspaceID string, runner portfwd.Runner) (string, error) {
	argv := ExecArgv(target, ctlPath, remoteNexus3HerdrListCmd())
	stdout, _, code, err := runner(ctx, argv)
	if err != nil {
		return "", fmt.Errorf("nexus3 herdr list: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("nexus3 herdr list: exit %d", code)
	}
	return parseSandboxIDFromSpaceList(stdout, workspaceID), nil
}

func parseSandboxIDFromSpaceList(output, workspaceID string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "(") {
			continue
		}
		fields := make(map[string]string)
		for _, part := range strings.Split(line, "\t") {
			k, v, ok := strings.Cut(part, "=")
			if ok {
				fields[k] = v
			}
		}
		if fields["workspace_id"] == workspaceID {
			return fields["sandbox_id"]
		}
	}
	return ""
}

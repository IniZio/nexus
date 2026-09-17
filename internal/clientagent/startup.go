package clientagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

type HerdrMachine struct {
	ProfileID string `json:"id"`
	SSHTarget string `json:"target"`
	Enabled   bool   `json:"enabled"`
	Selected  bool   `json:"selected"`
	Session   string `json:"session"`
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

var ExecCommandContext = exec.CommandContext

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
	logPath := filepath.Join(stateDir, "agent.log")
	if logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		defer logFile.Close()
		w := io.MultiWriter(os.Stderr, logFile)
		slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
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

var RemoteStateReader = ReadRemoteCombinedState
var ForwarderRunner portfwd.Runner = portfwd.OSRunner

var fallbackWarned  sync.Map
var prevFocusSandbox sync.Map

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
		if f.Sandbox == sandboxID {
			out = append(out, f)
		}
	}
	return out
}

func focusFromCombined(combined *RemoteCombinedState, _ string) (string, bool) {
	if combined.FocusMissing || combined.FocusState.SandboxID == "" {
		return "", true
	}
	return combined.FocusState.SandboxID, false
}

func Tick(ctx context.Context, stateDir string, managers map[string]*portfwd.Manager) error {
	machines, err := DiscoverHerdrMachines(ctx)
	if err != nil {
		return fmt.Errorf("machine list: %w", err)
	}
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
		combined, err := RemoteStateReader(ctx, ctlPath, m.SSHTarget)
		if err != nil {
			slog.Warn("local-agent-startup: read-remote-state", "target", m.SSHTarget, "err", err)
			continue
		}
		sandboxID, fallback := focusFromCombined(combined, m.SSHTarget)
		if !fallback && sandboxID != "" {
			prev, _ := prevFocusSandbox.Load(m.SSHTarget)
			if prevID, _ := prev.(string); prevID != sandboxID {
				slog.Info("portfwd focus: focused sandbox changed", "target", m.SSHTarget, "sandbox_id", sandboxID)
				prevFocusSandbox.Store(m.SSHTarget, sandboxID)
			}
		} else {
			prevFocusSandbox.Delete(m.SSHTarget)
		}
		focused := filterToFocused(combined.ForwardsState.Forwards, sandboxID, fallback)
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
			mgr := portfwd.NewManager(fw)
			if err := mgr.AdoptMasterForwards(ctx); err != nil {
				slog.Warn("local-agent-startup: adopt master forwards", "target", m.SSHTarget, "err", err)
			}
			managers[m.SSHTarget] = mgr
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

type RemoteCombinedState struct {
	ForwardsState RemoteForwardsState
	FocusState    portfwd.FocusState
	FocusMissing  bool
}

func remoteFocusStateFileShell() string {
	return "${XDG_STATE_HOME:-$HOME/.local/state}/nexus3/portfwd/focus.state"
}

// RemoteStateReadCommand returns the single remote command string.
// ONE argv element on purpose: ssh joins args with spaces, so a split
// {"sh","-c","cat <file>"} becomes `sh -c cat <file>` — cat reads stdin
// instead of the file (live bug 2026-09-15, engine-03 ↔ macOS herdr client).
// Sections are separated by a line containing exactly ---nexus3-focus--- so the
// parser can split them without ambiguity.
func RemoteStateReadCommand() string {
	return "cat " + portfwd.RemoteStateFileShell() + " 2>/dev/null || echo '{\"forwards\":[]}'; echo; echo ---nexus3-focus---; cat " + remoteFocusStateFileShell() + " 2>/dev/null; true"
}

func ReadRemoteCombinedState(ctx context.Context, ctlPath, target string) (*RemoteCombinedState, error) {
	return readRemoteCombinedStateWithRunner(ctx, ctlPath, target, portfwd.OSRunner)
}

func readRemoteCombinedStateWithRunner(ctx context.Context, ctlPath, target string, runner portfwd.Runner) (*RemoteCombinedState, error) {
	argv := ExecArgv(target, ctlPath, RemoteStateReadCommand())
	stdout, _, code, err := runner(ctx, argv)
	if err != nil {
		return nil, fmt.Errorf("ssh cat: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("ssh cat: exit %d", code)
	}
	return parseRemoteCombinedState(stdout)
}

func parseRemoteCombinedState(output string) (*RemoteCombinedState, error) {
	const sep = "\n---nexus3-focus---\n"
	idx := strings.Index(output, sep)
	var forwardsPart, focusPart string
	if idx < 0 {
		forwardsPart = strings.TrimSpace(output)
		focusPart = ""
	} else {
		forwardsPart = strings.TrimSpace(output[:idx])
		focusPart = strings.TrimSpace(output[idx+len(sep):])
	}
	if forwardsPart == "" {
		forwardsPart = `{"forwards":[]}`
	}
	var combined RemoteCombinedState
	if err := json.Unmarshal([]byte(forwardsPart), &combined.ForwardsState); err != nil {
		return nil, fmt.Errorf("parse forwards state: %w", err)
	}
	if focusPart == "" {
		combined.FocusMissing = true
		return &combined, nil
	}
	fs, err := portfwd.ParseFocusState([]byte(focusPart))
	if err != nil {
		combined.FocusMissing = true
		return &combined, nil
	}
	combined.FocusState = fs
	return &combined, nil
}

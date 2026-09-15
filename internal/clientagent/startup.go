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
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

// This file is the laptop side of auto port-forward. It must stay free of
// linux-only imports: the herdr startup hook runs on macOS, where the full
// nexus3 CLI does not build (cmd/nexus3-client is the darwin entry point).

// HerdrMachine is one entry from `herdr machine list --json`; only the fields
// the startup loop needs are decoded.
type HerdrMachine struct {
	ProfileID string `json:"id"`
	SSHTarget string `json:"target"` // "user@host" or an ssh_config Host alias
	Enabled   bool   `json:"enabled"`
	Selected  bool   `json:"selected"`
}

// StateDir is where the client agent keeps its ControlMaster sockets:
// $XDG_STATE_HOME/nexus3/portfwd-client (or ~/.local/state/...).
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "nexus3", "portfwd-client")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "nexus3", "portfwd-client")
}

// ResolveHerdrBin honours HERDR_BIN_PATH (injected by herdr into plugin
// processes) and falls back to PATH.
func ResolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", errors.New("herdr not found: HERDR_BIN_PATH is unset and no \"herdr\" binary is on PATH")
}

// ExecCommandContext is the process spawner used for `herdr machine list`;
// tests swap it.
var ExecCommandContext = exec.CommandContext

// SanitizeSSHTarget turns an SSH target into a filename component for the
// ControlMaster socket.
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

// RunStartup is the herdr >=0.9 startup hook body: every 5s it discovers
// nexus3 host machines from `herdr machine list --json`, keeps an SSH
// ControlMaster per machine, reads the host's forwards.state and reconciles
// `ssh -O forward` / `-O cancel` so guest ports appear at the same number on
// the laptop. Returns when ctx is cancelled.
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

// Tick performs one reconcile cycle across every enabled machine.
func Tick(ctx context.Context, stateDir string, managers map[string]*portfwd.Manager) error {
	machines, err := DiscoverHerdrMachines(ctx)
	if err != nil {
		return fmt.Errorf("machine list: %w", err)
	}

	for _, m := range machines {
		if !m.Enabled || m.SSHTarget == "" {
			continue
		}
		ctlPath := filepath.Join(stateDir, SanitizeSSHTarget(m.SSHTarget)+".ctl")
		fw := &portfwd.Forwarder{
			ControlPath: ctlPath,
			SSHHost:     m.SSHTarget,
			Run:         portfwd.OSRunner,
		}
		if err := fw.EnsureMaster(ctx); err != nil {
			slog.Warn("local-agent-startup: ensure-master", "target", m.SSHTarget, "err", err)
			continue
		}

		state, err := ReadRemoteForwardsState(ctx, ctlPath, m.SSHTarget)
		if err != nil {
			slog.Warn("local-agent-startup: read-remote-state", "target", m.SSHTarget, "err", err)
			continue
		}

		var desired []portfwd.Listener
		for _, fwd := range state.Forwards {
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

// DiscoverHerdrMachines runs `herdr machine list --json`.
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

// RemoteForwardEntry is the minimal shape of one forwards.state entry.
type RemoteForwardEntry struct {
	Port    uint16 `json:"port"`
	Sandbox string `json:"sandbox"`
	Status  string `json:"status"`
}

// RemoteForwardsState is the decoded forwards.state file of a nexus3 host.
type RemoteForwardsState struct {
	Forwards []RemoteForwardEntry `json:"forwards"`
}

// RemoteStateReadCommand is the single remote command string that reads the
// host's forwards.state, or prints an empty state when the file is absent.
//
// It is ONE argv element on purpose. ssh joins every remote-command argument
// with spaces and hands the result to the login shell, so a split
// {"sh", "-c", "cat <file> || echo ..."} arrives as `sh -c cat <file> ...`:
// sh runs `cat` with no file, cat reads an empty stdin, and the client parses
// "" as "unexpected end of JSON input" on every tick (live 2026-09-15 from a
// macOS herdr client against engine-03).
func RemoteStateReadCommand() string {
	return "cat " + portfwd.RemoteStateFileShell() + " 2>/dev/null || echo '{\"forwards\":[]}'"
}

// ReadRemoteForwardsState cats the host's forwards.state through the
// ControlMaster; an absent file reads as no forwards.
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

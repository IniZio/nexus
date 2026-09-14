//go:build linux

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	PFStatusIdle     = "idle"
	PFStatusLive     = "live"
	PFStatusPending  = "pending"
	PFStatusDead     = "dead"
	PFStatusError    = "error"
	PFStatusExpired  = "expired"
	PFStatusOutRange = "out_of_range"
)

const ForwardsStateFile = "forwards.state"
const paneStaleThreshold = 5 * time.Minute

const paneSep = "─────────────────────────────────────────────"

type ForwardsState struct {
	WrittenBy string        `json:"written_by"`
	UpdatedAt time.Time     `json:"updated_at"`
	Forwards  []PortForward `json:"forwards"`
}

type PortForward struct {
	Port        uint16    `json:"port"`
	Sandbox     string    `json:"sandbox"`
	Status      string    `json:"status"`
	ConfirmedAt time.Time `json:"confirmed_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

func LoadForwardsState(dir string) (*ForwardsState, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, ForwardsStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var s ForwardsState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, false, err
	}
	return &s, true, nil
}

func WriteForwardsStateAtomic(dir string, s *ForwardsState) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ForwardsStateFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ForwardsStateFile))
}

func RenderPortsPane(state *ForwardsState, cursor int, now time.Time) string {
	if state == nil {
		return "Port forwards — (no sandbox)\n" +
			paneSep + "\n" +
			"  laptop agent not connected\n" +
			"  forwards.state not found\n\n" +
			"  Run: nexus3 herdr agent attach <target>\n" +
			"  Setup: doc/portfwd-ssh-mechanism.md §1\n\n" +
			"  q  close pane\n"
	}

	var sb strings.Builder
	sandboxName := "none"
	if len(state.Forwards) > 0 {
		sandboxName = state.Forwards[0].Sandbox
	}
	sb.WriteString("Port forwards — " + sandboxName + "\n")
	sb.WriteString(paneSep + "\n")

	if len(state.Forwards) == 0 {
		sb.WriteString("  (no ports declared)\n\n")
		sb.WriteString("  agent connected " + state.UpdatedAt.Format("15:04:05") + "\n\n")
		sb.WriteString("  j/k  select   r  add port (recreates)   q  close\n")
		return sb.String()
	}

	for i, fwd := range state.Forwards {
		prefix := "  "
		if cursor == i {
			prefix = "> "
		}
		sb.WriteString(prefix + renderRow(fwd) + "\n")
	}

	if now.Sub(state.UpdatedAt) > paneStaleThreshold {
		mins := int(now.Sub(state.UpdatedAt).Minutes())
		sb.WriteString(fmt.Sprintf("  WARNING: state is %dm old — laptop agent may be down\n", mins))
	}

	sb.WriteString("\n")
	curStatus := ""
	if cursor >= 0 && cursor < len(state.Forwards) {
		curStatus = state.Forwards[cursor].Status
	}
	sb.WriteString("  " + paneFooter(curStatus) + "\n")
	return sb.String()
}

func renderRow(fwd PortForward) string {
	p := fmt.Sprintf("%d", fwd.Port)
	switch fwd.Status {
	case PFStatusLive:
		ts := fwd.ConfirmedAt.Format("15:04:05")
		return fmt.Sprintf("%s   LIVE    since %s   ctrl+click → http://127.0.0.1:%d", p, ts, fwd.Port)
	case PFStatusPending:
		return fmt.Sprintf("%s   PENDING request enqueued, waiting for laptop agent...", p)
	case PFStatusDead:
		ts := fwd.ConfirmedAt.Format("15:04:05")
		return fmt.Sprintf("%s   DEAD    lost %s   error: %s", p, ts, fwd.Error)
	case PFStatusError:
		return fmt.Sprintf("%s   ERROR   %s", p, fwd.Error)
	case PFStatusExpired:
		return fmt.Sprintf("%s   EXPIRED %s", p, fwd.Error)
	case PFStatusOutRange:
		return fmt.Sprintf("%s   OUT-OF-RANGE  recreate required (outside 1024-11023)", p)
	default:
		return fmt.Sprintf("%s   IDLE", p)
	}
}

func paneFooter(status string) string {
	switch status {
	case PFStatusLive:
		return "j/k  select   Enter  enqueue   r  add port (recreates)   q  close"
	case PFStatusDead:
		return "j/k  select   Enter  retry   r  add port (recreates)   q  close"
	case PFStatusPending:
		return "j/k  select   q  close"
	case PFStatusError:
		return "j/k  select   q  close"
	case PFStatusExpired:
		return "q  close"
	case PFStatusOutRange:
		return "j/k  select   r  add port (recreates)   q  close"
	case "":
		return "r  add port (recreates)   q  close"
	default:
		return "j/k  select   Enter  forward selected   r  add port (recreates)   q  close"
	}
}

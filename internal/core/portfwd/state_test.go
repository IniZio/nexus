package portfwd

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ── TestStateDir_IgnoresHerdrPluginStateDir ──
// Supervisor must not follow HERDR_PLUGIN_STATE_DIR (laptop client reads
// host path over plain ssh).
// MUTATION-PIN: honour HERDR_PLUGIN_STATE_DIR in StateDir → RED.
func TestStateDir_IgnoresHerdrPluginStateDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "herdr", "plugins", "nexus"))

	want := filepath.Join(xdg, "nexus", "portfwd")
	if got := StateDir(); got != want {
		t.Fatalf("StateDir() = %q, want %q (HERDR_PLUGIN_STATE_DIR must be ignored)", got, want)
	}
	if got := StateFile(); got != filepath.Join(want, "forwards.state") {
		t.Fatalf("StateFile() = %q", got)
	}
}

func TestStateDir_FallsBackToHomeLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "/should/be/ignored")

	want := filepath.Join(home, ".local", "state", "nexus", "portfwd")
	if got := StateDir(); got != want {
		t.Fatalf("StateDir() = %q, want %q", got, want)
	}
}

// ── TestRemoteStateFileShell_MatchesStateFile ──
// Shell expression the laptop client sends over ssh must expand to exactly
// the path supervisor writes.
// MUTATION-PIN: change either stateRelDir usage or shell prefix → RED.
func TestRemoteStateFileShell_MatchesStateFile(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	for _, tc := range []struct {
		name string
		xdg  string
	}{
		{"xdg-set", t.TempDir()},
		{"xdg-unset", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", tc.xdg)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", "/should/be/ignored")

			out, err := exec.Command("sh", "-c", "printf '%s' \""+RemoteStateFileShell()+"\"").Output()
			if err != nil {
				t.Fatalf("sh expand: %v", err)
			}
			expanded := strings.TrimSpace(string(out))
			if expanded != StateFile() {
				t.Fatalf("shell expression expands to %q but StateFile() = %q", expanded, StateFile())
			}
		})
	}
}

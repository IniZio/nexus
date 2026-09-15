package portfwd

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStateDir_IgnoresHerdrPluginStateDir pins the canonical, env-independent
// location: the supervisor must not follow HERDR_PLUGIN_STATE_DIR, because
// the laptop client reads the host path over a plain ssh session where that
// variable is never set.
//
// MUTATION PROOF: honour HERDR_PLUGIN_STATE_DIR in StateDir → RED.
func TestStateDir_IgnoresHerdrPluginStateDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "herdr", "plugins", "nexus3"))

	want := filepath.Join(xdg, "nexus3", "portfwd")
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

	want := filepath.Join(home, ".local", "state", "nexus3", "portfwd")
	if got := StateDir(); got != want {
		t.Fatalf("StateDir() = %q, want %q", got, want)
	}
}

// TestRemoteStateFileShell_MatchesStateFile proves writer and reader agree:
// the shell expression the laptop client sends over ssh expands (under the
// same HOME / XDG_STATE_HOME) to exactly the path the supervisor writes.
//
// MUTATION PROOF: change either stateRelDir usage or the shell prefix → RED.
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

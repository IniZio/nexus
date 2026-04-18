//go:build linux

package firecracker

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCheckTapHelperInstalled verifies that checkTapHelper returns a useful
// error message (including setup instructions) when nexus-tap-helper is absent.
func TestCheckTapHelperInstalled(t *testing.T) {
	if _, err := exec.LookPath(tapHelperBin); err == nil {
		// Helper is present — verify the happy path returns nil.
		if err := checkTapHelper(); err != nil {
			// May lack cap_net_admin in CI — that's still a valid non-nil return,
			// but the message must reference cap_net_admin.
			if !strings.Contains(err.Error(), "cap_net_admin") {
				t.Errorf("expected cap_net_admin error when setcap not applied, got: %v", err)
			}
		}
		return
	}

	// Helper absent — error must include setup instructions.
	err := checkTapHelper()
	if err == nil {
		t.Fatal("expected error when nexus-tap-helper is not installed")
	}
	if !strings.Contains(err.Error(), "nexus-tap-helper") {
		t.Errorf("expected error to mention nexus-tap-helper, got: %v", err)
	}
	if !strings.Contains(err.Error(), "setcap") {
		t.Errorf("expected error to include setcap setup instructions, got: %v", err)
	}
}

// TestCheckBridgeExists verifies that checkBridge returns a useful error
// message (including setup instructions) when nexusbr0 does not exist.
func TestCheckBridgeExists(t *testing.T) {
	out, err := exec.Command("ip", "link", "show", bridgeName).CombinedOutput()
	bridgePresent := err == nil && strings.Contains(string(out), "UP")

	if bridgePresent {
		// Bridge is present — verify happy path.
		if err := checkBridge(); err != nil {
			t.Errorf("checkBridge() returned error but bridge appears UP: %v", err)
		}
		return
	}

	// Bridge absent or down — error must include setup instructions.
	err = checkBridge()
	if err == nil {
		t.Fatal("expected error when nexusbr0 is not present/UP")
	}
	if !strings.Contains(err.Error(), bridgeName) {
		t.Errorf("expected error to mention %q, got: %v", bridgeName, err)
	}
	if !strings.Contains(err.Error(), "systemd-networkd") {
		t.Errorf("expected error to include systemd-networkd setup instructions, got: %v", err)
	}
}

func TestTapNameForWorkspace(t *testing.T) {
	const maxLen = 15
	id := "ws-12345678901234567890"
	got := tapNameForWorkspace(id)
	if len(got) > maxLen {
		t.Fatalf("tapNameForWorkspace(%q) len %d > %d", id, len(got), maxLen)
	}
	if got != tapNameForWorkspace(id) {
		t.Fatal("tap name must be deterministic")
	}
	other := tapNameForWorkspace("ws-12345678901234567891")
	if got == other {
		t.Fatalf("tap names must differ for different workspace ids: both %q", got)
	}
}

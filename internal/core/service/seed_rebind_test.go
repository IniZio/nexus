package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildUserMountScript_RebindOverExistingMount runs the generated rebind
// step in a rootless mount namespace where the guest path is already a
// mountpoint (the stop→start case) and checks the staged content wins, the
// result is read-only, and a second run is a no-op.
func TestBuildUserMountScript_RebindOverExistingMount(t *testing.T) {
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not available")
	}
	if err := exec.Command("unshare", "-rm", "true").Run(); err != nil {
		t.Skipf("rootless mount namespace unavailable: %v", err)
	}
	dir := t.TempDir()
	stag, other, guest := filepath.Join(dir, "stag"), filepath.Join(dir, "other"), filepath.Join(dir, "guest")
	for _, d := range []string{stag, other, guest} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stag, "PLUGIN"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "UPPER"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	script := buildUserMountScript(UserMountManifest{Mounts: []ResolvedUserMount{{
		HostPath: stag, GuestPath: guest, StagingGuestPath: stag, Rebind: true,
	}}})
	start := strings.Index(script, "# 1. Rebind")
	end := strings.Index(script, "# 2. PATH")
	if start < 0 || end < start {
		t.Fatalf("rebind step not found in script:\n%s", script)
	}
	step := script[start:end]

	harness := "set -eu\nmount --bind " + shSingleQuote(other) + " " + shSingleQuote(guest) + "\n" +
		step + step +
		"ls " + shSingleQuote(guest) + "\n" +
		"if touch " + shSingleQuote(filepath.Join(guest, "w")) + " 2>/dev/null; then echo WRITABLE; fi\n" +
		"grep -c " + shSingleQuote(guest) + " /proc/self/mounts\n"
	out, err := exec.Command("unshare", "-rm", "sh", "-c", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "PLUGIN") || strings.Contains(got, "UPPER") {
		t.Errorf("guest path must show staged content, got:\n%s", got)
	}
	if strings.Contains(got, "WRITABLE") {
		t.Errorf("rebind must be read-only, got:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "2") {
		t.Errorf("second run must not stack another bind (want 2 mounts on guest), got:\n%s", got)
	}
}

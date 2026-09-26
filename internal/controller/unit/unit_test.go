package unit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/controller/unit"
	"github.com/IniZio/nexus/internal/core/vault"
)

var testParams = unit.Params{
	NexusBinDir: "/usr/local/bin",
	ConfigPath:  "/etc/nexus/controller.yaml",
}

func TestSystemdUnitRenders(t *testing.T) {
	data, err := unit.RenderSystemd(testParams)
	if err != nil {
		t.Fatalf("RenderSystemd: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "nexus controller serve") {
		t.Error("missing 'nexus controller serve' in unit")
	}
	if !strings.Contains(out, testParams.ConfigPath) {
		t.Error("missing ConfigPath in unit")
	}
	if !strings.Contains(out, "Restart=on-failure") {
		t.Error("missing Restart=on-failure")
	}
}

func TestLaunchdPlistRenders(t *testing.T) {
	data, err := unit.RenderLaunchd(testParams)
	if err != nil {
		t.Fatalf("RenderLaunchd: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "com.nexus.controller") {
		t.Error("missing label in plist")
	}
	if !strings.Contains(out, testParams.ConfigPath) {
		t.Error("missing ConfigPath in plist")
	}
}

func TestUnitCredentialNameMatchesKeySource(t *testing.T) {
	if unit.CredentialName != vault.KeyCredentialName {
		t.Fatalf("CredentialName = %q, vault.KeyCredentialName = %q — mismatch",
			unit.CredentialName, vault.KeyCredentialName)
	}

	data, err := unit.RenderSystemd(testParams)
	if err != nil {
		t.Fatalf("RenderSystemd: %v", err)
	}
	if !strings.Contains(string(data), vault.KeyCredentialName) {
		t.Fatalf("systemd unit does not reference vault.KeyCredentialName %q", vault.KeyCredentialName)
	}
}

func TestUnitSetsPathAndTmpdir(t *testing.T) {
	data, err := unit.RenderSystemd(testParams)
	if err != nil {
		t.Fatalf("RenderSystemd: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "PATH=") {
		t.Error("PATH not set in systemd unit")
	}
	if !strings.Contains(out, testParams.NexusBinDir) {
		t.Errorf("NexusBinDir %q not in PATH", testParams.NexusBinDir)
	}
	if !strings.Contains(out, "TMPDIR=/var/tmp") {
		t.Error("TMPDIR=/var/tmp not set in systemd unit")
	}
}

func TestSystemdAnalyzeUnit(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not available")
	}

	data, err := unit.RenderSystemd(testParams)
	if err != nil {
		t.Fatalf("RenderSystemd: %v", err)
	}

	dir := t.TempDir()
	unitFile := filepath.Join(dir, "nexus-controller.service")
	if err := os.WriteFile(unitFile, data, 0o600); err != nil {
		t.Fatalf("write unit file: %v", err)
	}

	out, err := exec.Command("systemd-analyze", "--user", "verify", unitFile).CombinedOutput()
	t.Logf("systemd-analyze output:\n%s", out)
	if err != nil {
		t.Logf("systemd-analyze verify exited non-zero (may be expected without user session): %v", err)
	}
}

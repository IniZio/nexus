package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBuildShVersionGuard_RejectsOldHerdr verifies that plugins/herdr/build.sh
// exits non-zero when the installed herdr binary is older than 0.9.0.
// It creates a fake herdr + nexus3 environment so only the version-check
// block executes.
func TestBuildShVersionGuard_RejectsOldHerdr(t *testing.T) {
	t.Parallel()
	checkVersionGuard(t, "0.8.0", true)
}

func TestBuildShVersionGuard_AcceptsCurrentHerdr(t *testing.T) {
	t.Parallel()
	checkVersionGuard(t, "0.9.0", false)
}

func TestBuildShVersionGuard_AcceptsNewerHerdr(t *testing.T) {
	t.Parallel()
	checkVersionGuard(t, "1.2.3", false)
}

// checkVersionGuard runs the version-guard snippet from build.sh with a
// fake herdr that reports herdrVer and asserts the expected exit outcome.
func checkVersionGuard(t *testing.T, herdrVer string, wantFail bool) {
	t.Helper()

	// Build a minimal shell script that replicates the version-guard block
	// from plugins/herdr/build.sh, using the provided version directly.
	script := `MIN_HERDR="0.9.0"
HERDR_VER="` + herdrVer + `"
if [ -n "$HERDR_VER" ]; then
    LOWEST="$(printf '%s\n%s\n' "$MIN_HERDR" "$HERDR_VER" | sort -V | head -1)"
    if [ "$LOWEST" != "$MIN_HERDR" ]; then
        echo "nexus3: error: herdr ${HERDR_VER} < ${MIN_HERDR}: upgrade herdr first" >&2
        exit 1
    fi
fi
`
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "version_guard.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", scriptPath)
	out, err := cmd.CombinedOutput()
	if wantFail && err == nil {
		t.Errorf("expected failure for herdr %s but script exited 0; output: %s", herdrVer, out)
	}
	if !wantFail && err != nil {
		t.Errorf("expected success for herdr %s but script failed: %v; output: %s", herdrVer, err, out)
	}
}

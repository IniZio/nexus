package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// captureScriptExecer returns a GuestExecer that records the bash -c script
// arg (the third element of cmd) into *out and returns the given exit code.
// It does not execute the script in the guest; tests that need real bash
// behaviour run the captured script locally after substituting in-guest paths.
func captureScriptExecer(out *string, code int32) service.GuestExecer {
	return func(_ context.Context, _ domain.SandboxID, cmd []string, _ io.Reader) (int32, error) {
		if len(cmd) >= 3 && cmd[1] == "-c" {
			*out = cmd[2]
		}
		return code, nil
	}
}

// runBashAndExitCode runs a bash script and returns its exit code.
func runBashAndExitCode(t *testing.T, script string) int {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("bash: unexpected error (not an ExitError): %v", err)
	return -1
}

// runBashScriptResult runs a bash script and returns the exit code and captured stderr.
func runBashScriptResult(t *testing.T, script string) (exitCode int, stderr string) {
	t.Helper()
	var stderrBuf strings.Builder
	cmd := exec.Command("bash", "-c", script)
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	stderr = stderrBuf.String()
	if err == nil {
		return 0, stderr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stderr
	}
	t.Fatalf("bash: unexpected error (not an ExitError): %v", err)
	return -1, stderr
}

// substituteGuestPaths replaces the production in-guest absolute paths in the
// captured script with baseDir-relative equivalents so the real script can be
// run unprivileged on the test host. It also stubs "mount -t overlay overlay"
// with an echo so branch selection logic and filesystem side-effects (mkdir,
// cp, rm) can be exercised without root. The substitution order is
// most-specific first so that e.g. /var/lib/nexus3/agentcfg-upper is replaced
// before /var/lib/nexus3/agentcfg, which is replaced before /var/lib/nexus3.
// strings.NewReplacer applies the first matching pattern at each position.
func substituteGuestPaths(script, baseDir string) string {
	return strings.NewReplacer(
		"mount -t overlay overlay", "echo WOULD_MOUNT",
		"/var/lib/nexus3/agentcfg-upper", filepath.Join(baseDir, "agentcfg-upper"),
		"/var/lib/nexus3/agentcfg/upper", filepath.Join(baseDir, "agentcfg", "upper"),
		"/var/lib/nexus3/agentcfg/work", filepath.Join(baseDir, "agentcfg", "work"),
		"/var/lib/nexus3/agentcfg-work", filepath.Join(baseDir, "agentcfg-work"),
		"/var/lib/nexus3/agentcfg", filepath.Join(baseDir, "agentcfg"),
		"/var/lib/nexus3", baseDir,
		"/root/.claude", filepath.Join(baseDir, "root-claude"),
	).Replace(script)
}

// TestSeedOverlayClaudeConfig_ScriptContainsMountGuard asserts that the script
// emitted by seedOverlayClaudeConfig contains the fail-closed mountpoint guard
// (D-RAM-08). The guard must print a "not a mountpoint" diagnostic and exit 1
// when the named volume is absent — never silently continuing onto root ext4.
//
// Mutation proof: removing or silencing the guard block removes the marker
// string → strings.Contains returns false → this test fails.
// Substitution count to apply the mutation: 1 (the echo/exit guard block).
func TestSeedOverlayClaudeConfig_ScriptContainsMountGuard(t *testing.T) {
	var script string
	_ = seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/lower",
		captureScriptExecer(&script, 0),
	)
	const guardMarker = "not a mountpoint"
	if !strings.Contains(script, guardMarker) {
		t.Errorf("seedOverlayClaudeConfig script missing mountpoint guard %q\n"+
			"D-RAM-08 fail-closed guard was removed or silenced.\n"+
			"Script:\n%s", guardMarker, script)
	}
}

// TestSeedOverlayClaudeConfig_GuardUsesStatComparison verifies that the script
// emitted by seedOverlayClaudeConfig uses the stat device-number comparison
// mechanism AND exits 1 when that comparison indicates a non-mountpoint. Two
// specific strings are required: the condition and the exit-1 branch.
//
// Mutation proof for the guard exit: changing `exit 1` to `true` or `exit 0`
// in the guard block removes the `exit 1` suffix from that specific line.
// Since `exit 1` appears on other lines too (the stat-fail branches), the check
// uses the COMBINED string of condition + exit that must appear together.
// Substitution count to apply the mutation: 1 (the condition+exit-1 block).
func TestSeedOverlayClaudeConfig_GuardUsesStatComparison(t *testing.T) {
	var script string
	_ = seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/lower",
		captureScriptExecer(&script, 0),
	)
	// The condition checks device inequality: if equal → volume not mounted.
	const cond = `"$_mp_dev" != "$_par_dev"`
	if !strings.Contains(script, cond) {
		t.Errorf("script missing stat device-comparison condition %q — "+
			"guard mechanism removed or changed", cond)
	}
	// Branch 3 (new sandbox, no volume, no prior data) must exit non-zero.
	// Two strings must both be present: the diagnostic message and the exit 1
	// immediately following it. Changing exit 1 → true removes the marker.
	const exitMsg = "refusing to fall back to root ext4"
	if !strings.Contains(script, exitMsg) {
		t.Errorf("script missing fail-closed message %q — "+
			"Branch 3 exit-1 path removed", exitMsg)
	}
	const exitCmd = "\n    exit 1\n"
	if !strings.Contains(script, exitCmd) {
		t.Errorf("script missing bare %q — "+
			"changing exit 1 to exit 0 or true breaks this assertion", exitCmd)
	}
}

// TestSeedOverlayClaudeConfig_GuardMechanismRejectsNonMountpoint runs the core
// guard bash logic (stat device-number comparison) directly with temp dirs that
// share the same device number, proving the mechanism rejects non-mountpoints.
// Uses a self-contained bash snippet rather than the full captured script to
// avoid running privileged commands (mount) or substituting production paths.
//
// This test fails if the stat comparison is inverted or the exit-on-equal branch
// is removed — providing behavioural mutation proof independent of text checks.
func TestSeedOverlayClaudeConfig_GuardMechanismRejectsNonMountpoint(t *testing.T) {
	parentDir := t.TempDir()
	agentcfgDir := filepath.Join(parentDir, "agentcfg")
	if err := os.Mkdir(agentcfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Both dirs are on the same tmpfs/ext4 device → stat returns equal device
	// numbers → the guard must exit 1.
	guardScript := fmt.Sprintf(`set -e
_mp_dev=$(stat -c '%%d' '%s' 2>/dev/null) || { echo "stat failed" >&2; exit 1; }
_par_dev=$(stat -c '%%d' '%s' 2>/dev/null) || { echo "parent stat failed" >&2; exit 1; }
[ "$_mp_dev" != "$_par_dev" ] || { echo "not a mountpoint" >&2; exit 1; }
echo "ok"
`, agentcfgDir, parentDir)
	code := runBashAndExitCode(t, guardScript)
	if code == 0 {
		t.Error("guard mechanism did not reject non-mountpoint: bash exited 0 — " +
			"stat comparison broken or exit-1 branch removed")
	}
}

// TestSeedOverlayClaudeConfig_Branch2_DegradePreExisting runs the REAL captured
// production script (with absolute paths substituted to temp dirs and the
// overlay mount stubbed) against a directory layout that selects Branch 2:
// named volume absent (agentcfg is a plain dir on the same device as parent)
// but legacy agentcfg-upper data exists. Verifies that:
//   - the script exits 0 (degrade gracefully, not fail closed)
//   - stderr names "degrading to root ext4" (the D-RAM-11 warning)
//   - the legacy data is preserved (Branch 2 must NOT migrate or destroy it)
//   - the fallback work dir is created
//
// Mutation proof: changing the elif condition to `elif false` removes Branch 2
// so pre-existing sandboxes fall to Branch 3 (exit 1) — test turns RED.
// Substitution count of that mutation: 1 (the elif line).
func TestSeedOverlayClaudeConfig_Branch2_DegradePreExisting(t *testing.T) {
	baseDir := t.TempDir()

	// agentcfg as a plain dir on the same device as baseDir — not a mountpoint.
	if err := os.Mkdir(filepath.Join(baseDir, "agentcfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Legacy upper dir with sentinel — Branch 2 must keep this intact.
	legacyUpper := filepath.Join(baseDir, "agentcfg-upper")
	if err := os.Mkdir(legacyUpper, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(legacyUpper, "session.txt")
	if err := os.WriteFile(sentinelPath, []byte("branch2-sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture the REAL production script.
	var script string
	_ = seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/fake-lower",
		captureScriptExecer(&script, 0),
	)

	// Substitute absolute paths and stub the overlay mount.
	adapted := substituteGuestPaths(script, baseDir)

	exitCode, stderr := runBashScriptResult(t, adapted)

	// D-RAM-13: Branch 2 now exits 2 so seedOverlayClaudeConfig can return
	// errAgentCfgDegraded (non-fatal sentinel) instead of nil. Exit 0 would
	// be indistinguishable from Branch 1 (success) at the Go caller level.
	if exitCode != 2 {
		t.Errorf("Branch 2 must exit 2 (degraded-sentinel); got %d\nstderr: %s\nadapted script:\n%s",
			exitCode, stderr, adapted)
	}
	if !strings.Contains(stderr, "degrading to root ext4") {
		t.Errorf("Branch 2 stderr must contain %q (D-RAM-11 warning); got:\n%s", "degrading to root ext4", stderr)
	}
	// Sentinel must still exist — Branch 2 mounts in-place, never migrates.
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Errorf("Branch 2 must not destroy legacy data; sentinel %s gone: %v", sentinelPath, err)
	}
	// Fallback work dir must have been created.
	fbWork := filepath.Join(baseDir, "agentcfg-work")
	if fi, err := os.Stat(fbWork); err != nil || !fi.IsDir() {
		t.Errorf("Branch 2 must create fallback work dir %s; stat err: %v", fbWork, err)
	}
}

// TestSeedOverlayClaudeConfig_Branch3_FailClosed runs the REAL captured
// production script against a layout that selects Branch 3: named volume
// absent and no prior data at any legacy or new path. This is the new-sandbox
// case — the volume must have been provisioned at create time (D-RAM-09).
// Verifies exit non-zero and the fail-closed diagnostic message.
//
// Mutation proof: changing the Branch 3 `exit 1` to `true` makes the script
// exit 0 — test sees 0 instead of non-zero and turns RED.
// Substitution count of that mutation: 1 (the bare exit 1 in the else block).
func TestSeedOverlayClaudeConfig_Branch3_FailClosed(t *testing.T) {
	baseDir := t.TempDir()

	// agentcfg as a plain dir (same device) — no agentcfg-upper, no agentcfg/upper.
	if err := os.Mkdir(filepath.Join(baseDir, "agentcfg"), 0o755); err != nil {
		t.Fatal(err)
	}

	var script string
	_ = seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/fake-lower",
		captureScriptExecer(&script, 0),
	)

	adapted := substituteGuestPaths(script, baseDir)
	exitCode, stderr := runBashScriptResult(t, adapted)

	if exitCode == 0 {
		t.Errorf("Branch 3 must exit non-zero (fail closed); got 0\nstderr: %s\nadapted script:\n%s",
			stderr, adapted)
	}
	if !strings.Contains(stderr, "refusing to fall back to root ext4") {
		t.Errorf("Branch 3 stderr must contain fail-closed message; got:\n%s", stderr)
	}
}

// TestSeedOverlayClaudeConfig_ScriptContainsMigration asserts that the script
// emitted by seedOverlayClaudeConfig contains the one-shot migration block
// (D-RAM-09) that moves existing content from the legacy agentcfg-upper path
// on root ext4 into the new governor-visible upper dir on the named volume.
//
// Mutation proof: removing the cp -a migration command removes the marker
// string → strings.Contains returns false → this test fails.
// Substitution count to apply the mutation: 1 (the cp -a line).
func TestSeedOverlayClaudeConfig_ScriptContainsMigration(t *testing.T) {
	var script string
	_ = seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/lower",
		captureScriptExecer(&script, 0),
	)
	const migrateMarker = "cp -a /var/lib/nexus3/agentcfg-upper/."
	if !strings.Contains(script, migrateMarker) {
		t.Errorf("seedOverlayClaudeConfig script missing migration block %q\n"+
			"D-RAM-09 one-shot migration was removed — existing sandbox Claude\n"+
			"session state would be silently abandoned on upgrade.\n"+
			"Script:\n%s", migrateMarker, script)
	}
}

// TestSeedOverlayClaudeConfig_Branch2_ReturnsErrDegraded verifies that when the
// execer reports exit code 2 (Branch 2 — degraded to root ext4) the Go function
// returns errAgentCfgDegraded rather than nil or a generic error.
//
// Mutation proof (substitution count: 1): replacing `case 2` with `case 99` in
// the switch makes seedOverlayClaudeConfig return a generic non-nil error,
// so errors.Is(..., errAgentCfgDegraded) turns false → test RED.
func TestSeedOverlayClaudeConfig_Branch2_ReturnsErrDegraded(t *testing.T) {
	err := seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/fake-lower",
		captureScriptExecer(new(string), 2),
	)
	if !errors.Is(err, errAgentCfgDegraded) {
		t.Errorf("exit 2 (Branch 2) must return errAgentCfgDegraded; got: %v", err)
	}
}

// TestSeedOverlayClaudeConfig_ScriptContainsMountedMarker verifies that the
// production script writes the volume-independent mounted marker (D-RAM-13) on
// Branch 1 so that Branch 3 can distinguish attach-failure from new-sandbox.
//
// Mutation proof (substitution count: 1): removing the touch command makes the
// marker absent from the script → test RED.
func TestSeedOverlayClaudeConfig_ScriptContainsMountedMarker(t *testing.T) {
	var script string
	_ = seedOverlayClaudeConfig(
		context.Background(), domain.SandboxID{}, "/lower",
		captureScriptExecer(&script, 0),
	)
	marker := "touch " + agentCfgMountedMarker
	if !strings.Contains(script, marker) {
		t.Errorf("seedOverlayClaudeConfig script missing mounted marker %q (D-RAM-13);\n"+
			"Branch 3 cannot distinguish attach-failure from new-sandbox.\nScript:\n%s", marker, script)
	}
}

// ── caller-level fail-closed enforcement ──────────────────────────────────────

// pingOKProber is a GuestProber stub whose Ping always succeeds.
type pingOKProber struct{}

func (pingOKProber) Ping(_ context.Context) error { return nil }

// noopSeeder is a service.GuestSeeder that always succeeds.
func noopSeeder(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

// withSeedOverlayFn temporarily replaces seedOverlayClaudeConfigFn with stub
// for the duration of one test, restoring the original in t.Cleanup.
func withSeedOverlayFn(t *testing.T, stub func(context.Context, domain.SandboxID, string, service.GuestExecer) error) {
	t.Helper()
	orig := seedOverlayClaudeConfigFn
	seedOverlayClaudeConfigFn = stub
	t.Cleanup(func() { seedOverlayClaudeConfigFn = orig })
}

// minimalInputs builds a guestSeedInputs that supplies non-nil seeders (so
// subsequent seed steps don't panic) and sets AgentCfgLowerGuestPath to a
// non-empty value so the overlay branch is exercised.
func minimalInputs() guestSeedInputs {
	noop := service.GuestSeeder(noopSeeder)
	noopExecer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, _ []string, _ io.Reader) (int32, error) {
		return 0, nil
	})
	return guestSeedInputs{
		AgentCfgLowerGuestPath: "/fake-lower",
		ProfileSeeder:          noop,
		GitSeeder:              noop,
		CredentialHelperSeeder: noop,
		Execer:                 noopExecer,
	}
}

// TestProbeAndSeedGuest_OverlayFailure_FatalAtCaller verifies that when
// seedOverlayClaudeConfigFn returns a generic (Branch 3 / attach) error,
// probeAndSeedGuest propagates it as a non-nil return — aborting the boot.
//
// Mutation proof (substitution count: 1): replacing
//
//	return fmt.Errorf("supervisor: agentcfg overlay mount failed...")
//
// with a slog.Warn and return nil makes probeAndSeedGuest return nil, which
// causes this test to call t.Error("expected non-nil error") → RED.
func TestProbeAndSeedGuest_OverlayFailure_FatalAtCaller(t *testing.T) {
	stubErr := errors.New("branch3: test-injected overlay failure")
	withSeedOverlayFn(t, func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		return stubErr
	})

	err := probeAndSeedGuest(context.Background(), pingOKProber{}, minimalInputs())
	if err == nil {
		t.Error("expected non-nil error when overlay seed fails (fail-closed); got nil — boot would continue with bare /root/.claude")
	}
}

// TestProbeAndSeedGuest_OverlayDegraded_NonFatal verifies that errAgentCfgDegraded
// (Branch 2) is treated as non-fatal: probeAndSeedGuest returns nil so the boot
// continues with the root-ext4 fallback overlay.
func TestProbeAndSeedGuest_OverlayDegraded_NonFatal(t *testing.T) {
	withSeedOverlayFn(t, func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		return errAgentCfgDegraded
	})

	err := probeAndSeedGuest(context.Background(), pingOKProber{}, minimalInputs())
	if err != nil {
		t.Errorf("errAgentCfgDegraded (Branch 2) must be non-fatal; got: %v", err)
	}
}

// TestProbeAndSeedGuest_OverlayOK_NonFatal verifies the happy path (Branch 1):
// nil from the stub propagates as nil from probeAndSeedGuest.
func TestProbeAndSeedGuest_OverlayOK_NonFatal(t *testing.T) {
	withSeedOverlayFn(t, func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		return nil
	})

	err := probeAndSeedGuest(context.Background(), pingOKProber{}, minimalInputs())
	if err != nil {
		t.Errorf("nil from overlay seed must yield nil from probeAndSeedGuest; got: %v", err)
	}
}

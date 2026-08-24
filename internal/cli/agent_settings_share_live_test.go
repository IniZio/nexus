//go:build herdr_live

// TestAgentSettingsShareLive codifies the A-MOUNT acceptance criteria end-to-end.
//
// # What is under test
//
// When `nexus3 create --agent claude-code` is called, cmd_sandbox.go assembles
// a curated, secret-free copy of the user's ~/.claude via
// service.AssembleCuratedConfig and adds it as a RO live mount at
// /run/nexus3/agentcfg-lower BEFORE service.CreateAndBoot. Inside the VM,
// probeAndSeedGuest mounts a writable overlayfs onto /root/.claude (lowerdir =
// the virtiofs share; upperdir = tmpfs) before any onboarding seeds run.
//
// This test sets HOME in the subprocess env to a fake dir containing marker
// files (CLAUDE.md, skills/demo/SKILL.md, settings.json, .credentials.json).
// No --mount flag is passed — the wiring must come from the --agent code path.
//
// Acceptance criteria (A-MOUNT):
//
//  1. /root/.claude/CLAUDE.md shows the shared marker content (lowerdir visible).
//  2. /root/.claude/skills/demo/SKILL.md is present.
//  3. /root/.claude is writable (writes land in the tmpfs upper layer).
//  4. .credentials.json is NOT present in /root/.claude (hard-excluded by allowlist).
//  5. Host CLAUDE.md is byte-identical after sandbox exits (host unchanged).
//  6. --no-share-settings prevents the overlay (shared marker absent).
//
// Mutation guard: assertions 1–4 fail if the pre-CreateAndBoot wiring is removed.
//
// Run:
//
//	TMPDIR=/tmp NEXUS3_KERNEL_PATH=$(pwd)/images/kernel/vmlinux-x86_64 \
//	  NEXUS3_LIVE_REQUIRED=1 \
//	  go test -tags herdr_live ./internal/cli/... -run TestAgentSettingsShareLive -v -count=1
//
// Prerequisites:
//   - /dev/kvm must be available
//   - NEXUS3_KERNEL_PATH must be set to a vmlinux image
//   - nexus3-agent-base image must be locally cached (or set NEXUS3_SHARE_IMAGE)
package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realStateHome resolves the durable state dir the nexus3 image store lives
// under, so the live-test subprocesses can reach the operator's cached
// nexus3-agent-base image. store.DefaultRoot() honours XDG_STATE_HOME when set,
// else falls back to $HOME/.local/state.
//
// Two traps make this non-trivial:
//
//  1. This package's TestMain (testmain_test.go) clobbers XDG_STATE_HOME to a
//     throwaway temp dir for the whole run, so reading XDG_STATE_HOME IN-PROCESS
//     yields that empty temp store — never the operator's real one. We therefore
//     do NOT read XDG_STATE_HOME here.
//  2. The subprocess overrides HOME to a fake dir (so AssembleCuratedConfig
//     reads the fake ~/.claude); if the image store were left to follow that
//     fake HOME it would land where nexus3-agent-base is not cached — the prior
//     bug that surfaced as "no cached image" on a clean run.
//
// So we PIN XDG_STATE_HOME on the subprocess to the operator's real state dir:
// NEXUS3_SHARE_STATE_HOME when the operator sets it (escape hatch for a custom
// XDG layout), else $HOME/.local/state resolved from the test process's real
// HOME (which is NOT overridden at the process level — only per subprocess).
func realStateHome() string {
	if v := os.Getenv("NEXUS3_SHARE_STATE_HOME"); v != "" {
		return v
	}
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "state")
	}
	return ""
}

// shareCmdFakeHome builds a nexus3 subprocess with:
//   - XDG_STATE_HOME pinned to the real state dir (so the real image store is
//     reachable even though HOME is faked)
//   - HOME overridden to fakeHome (so AssembleCuratedConfig reads the fake .claude)
func shareCmdFakeHome(binary, fakeHome string, args ...string) *exec.Cmd {
	realState := realStateHome()
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			continue
		}
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+fakeHome)
	if realState != "" {
		env = append(env, "XDG_STATE_HOME="+realState)
	}

	cmd := exec.Command(binary, args...)
	cmd.Env = env
	return cmd
}

// shareCmd builds a nexus3 subprocess with XDG_STATE_HOME pinned to the real
// state dir so lifecycle verbs (exec, rm) address the same image store and
// sandbox records that shareCmdFakeHome created.
func shareCmd(binary string, args ...string) *exec.Cmd {
	realState := realStateHome()
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	if realState != "" {
		env = append(env, "XDG_STATE_HOME="+realState)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	return cmd
}

func TestAgentSettingsShareLive(t *testing.T) {
	if os.Getenv("NEXUS3_LIVE_REQUIRED") == "" {
		t.Skip("set NEXUS3_LIVE_REQUIRED=1 to run live tests (requires KVM + built images)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skipf("share: /dev/kvm not available: %v", err)
	}
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		t.Skip("share: NEXUS3_KERNEL_PATH is not set; set it to a vmlinux image to run this test")
	}

	// Build the nexus3 binary.
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-share")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("share: nexus3 binary cannot be built: %v\n%s", err, out)
	}

	image := os.Getenv("NEXUS3_SHARE_IMAGE")
	if image == "" {
		image = "nexus3-agent-base"
	}

	// --- Prepare fake HOME/.claude with marker files. ---
	//
	// HOME is overridden in the subprocess env so service.AssembleCuratedConfig
	// picks up these files (expandTilde reads HOME). The .credentials.json file
	// is planted to prove the allowlist excludes it.
	fakeHome := t.TempDir()
	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir fake .claude: %v", err)
	}

	const claudeMDContent = "# Shared CLAUDE.md marker for TestAgentSettingsShareLive\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(claudeMDContent), 0o644); err != nil {
		t.Fatalf("write fake CLAUDE.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(claudeDir, "skills", "demo"), 0o755); err != nil {
		t.Fatalf("mkdir fake skills/demo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "skills", "demo", "SKILL.md"),
		[]byte("# Demo skill\nThis skill is a test marker.\n"), 0o644); err != nil {
		t.Fatalf("write fake SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"autoApprove":[]}`+"\n"), 0o644); err != nil {
		t.Fatalf("write fake settings.json: %v", err)
	}
	// .credentials.json: MUST be excluded by AssembleCuratedConfig allowlist.
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"),
		[]byte(`{"fake":"secret-must-not-appear-in-guest"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write fake .credentials.json: %v", err)
	}

	// Snapshot for host-unchanged assertion after sandbox exits.
	hostCLAUDEMD, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read host CLAUDE.md snapshot: %v", err)
	}

	// --- Primary test: create --agent claude-code (NO --mount). ---
	//
	// The A-MOUNT wiring in cmd_sandbox.go must pick up the fake HOME/.claude
	// and inject it automatically. If the pre-CreateAndBoot block is removed,
	// /root/.claude/CLAUDE.md will be absent and the tracer token won't appear.
	handle := fmt.Sprintf("sharetest/overlay-%d", time.Now().UnixMilli())

	t.Cleanup(func() {
		rmOut, rmErr := shareCmd(binary, "rm", handle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", handle, rmOut)
		}
	})

	createOut, createErr := shareCmdFakeHome(binary, fakeHome,
		"create", handle,
		"--agent", "claude-code",
		"--image", image,
		"--no-builtin-gh",
	).CombinedOutput()
	if createErr != nil {
		t.Fatalf("nexus3 create: %v\n%s\n(check NEXUS3_KERNEL_PATH and that %q is cached)", createErr, createOut, image)
	}
	t.Logf("nexus3 create:\n%s", createOut)

	// --- In-guest verification script. ---
	//
	// Prints SHARE_TRACER_OK only when all assertions pass so matching the
	// token proves full script completion rather than partial output.
	const tracerToken = "SHARE_TRACER_OK"
	script := `set -euo pipefail

# AC-1: CLAUDE.md must be present with the shared marker.
if [ ! -f /root/.claude/CLAUDE.md ]; then
  echo "FAIL: /root/.claude/CLAUDE.md missing" >&2; exit 1
fi
if ! grep -q 'Shared CLAUDE.md marker' /root/.claude/CLAUDE.md; then
  echo "FAIL: /root/.claude/CLAUDE.md does not contain shared marker" >&2
  echo "got: $(cat /root/.claude/CLAUDE.md)" >&2
  exit 1
fi

# AC-2: skills/demo/SKILL.md must be present.
if [ ! -f /root/.claude/skills/demo/SKILL.md ]; then
  echo "FAIL: /root/.claude/skills/demo/SKILL.md missing" >&2; exit 1
fi

# AC-3: /root/.claude must be writable (writes land in tmpfs upper).
touch /root/.claude/nexus3-write-test-$$
rm -f /root/.claude/nexus3-write-test-$$

# AC-4: .credentials.json MUST NOT be present (excluded by allowlist).
if [ -f /root/.claude/.credentials.json ]; then
  echo "FAIL: .credentials.json present in guest — allowlist did not exclude it" >&2
  exit 1
fi

echo ` + tracerToken + `
`
	execOut, execErr := shareCmd(binary, "exec", handle, "--", "/bin/bash", "-c", script).CombinedOutput()
	t.Logf("exec output:\n%s", execOut)
	if execErr != nil {
		t.Fatalf("nexus3 exec: %v\n%s", execErr, execOut)
	}

	if !bytes.Contains(execOut, []byte(tracerToken)) {
		t.Errorf("tracer token %q absent — overlay not wired or script failed\n%s", tracerToken, execOut)
	}

	// --- AC-5: Host CLAUDE.md byte-identical after sandbox exec. ---
	afterCLAUDEMD, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read host CLAUDE.md after exec: %v", err)
	}
	if !bytes.Equal(hostCLAUDEMD, afterCLAUDEMD) {
		t.Errorf("host CLAUDE.md mutated by sandbox:\nbefore: %q\nafter:  %q", hostCLAUDEMD, afterCLAUDEMD)
	}

	// --- AC-6: --no-share-settings prevents the overlay. ---
	noShareHandle := fmt.Sprintf("sharetest/noshare-%d", time.Now().UnixMilli())
	t.Cleanup(func() {
		rmOut, rmErr := shareCmd(binary, "rm", noShareHandle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", noShareHandle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", noShareHandle, rmOut)
		}
	})

	noShareOut, noShareErr := shareCmdFakeHome(binary, fakeHome,
		"create", noShareHandle,
		"--agent", "claude-code",
		"--image", image,
		"--no-builtin-gh",
		"--no-share-settings",
	).CombinedOutput()
	if noShareErr != nil {
		t.Fatalf("nexus3 create (no-share): %v\n%s", noShareErr, noShareOut)
	}
	t.Logf("nexus3 create (no-share):\n%s", noShareOut)

	checkNoShare := `set -euo pipefail
if [ -f /root/.claude/CLAUDE.md ] && grep -q 'Shared CLAUDE.md marker' /root/.claude/CLAUDE.md; then
  echo "FAIL: shared marker present in --no-share-settings sandbox" >&2; exit 1
fi
echo NO_SHARE_OK
`
	nsOut, nsErr := shareCmd(binary, "exec", noShareHandle, "--", "/bin/bash", "-c", checkNoShare).CombinedOutput()
	t.Logf("no-share exec: %s", nsOut)
	if nsErr != nil {
		t.Fatalf("nexus3 exec (no-share): %v\n%s", nsErr, nsOut)
	}
	if !bytes.Contains(nsOut, []byte("NO_SHARE_OK")) {
		t.Errorf("--no-share-settings sandbox unexpectedly has shared marker\n%s", nsOut)
	}
}

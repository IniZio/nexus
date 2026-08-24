//go:build herdr_live

// TestAgentSettingsMCPShareLive codifies the C-SECRET / D-PP-04 acceptance
// criteria for MCP server definition sharing end-to-end.
//
// # What is under test
//
// When `nexus3 create --agent claude-code` is called with a host ~/.claude.json
// that contains a top-level mcpServers entry, the create path:
//  1. Reads the entry via BuildSharedMCPServers (gated on MCPConfigFormatClaudeJSON).
//  2. Writes it to stageDir/mcp-servers.json alongside the curated A-MOUNT overlay.
//  3. At boot, the supervisor reads that file and calls SeedGuestMCPServers, which
//     node-merges the servers map into the guest's /root/.claude.json mcpServers.
//  4. For stdio servers: ${VAR} env references are resolved from the host process
//     env and written as KEY=VALUE into the guest cred.env (D-PP-04 relaxation).
//     The .claude.json entry retains the ${VAR} reference verbatim — not the value.
//
// Acceptance criteria:
//
//  1. /root/.claude.json mcpServers contains key "nexus3-probe" with command "cat"
//     and env.PROBE_TOKEN == "${PROBE_TOKEN}" (verbatim ref, NOT the secret value).
//  2. /run/nexus3/cred.env contains PROBE_TOKEN=probe-secret-value (stdio plaintext).
//  3. claude mcp list (via login shell) lists "nexus3-probe" (definition reached Claude).
//  4. .credentials.json is NOT present in /root/.claude (hard-deny intact).
//  5. --no-share-settings prevents injection: nexus3-probe absent from /root/.claude.json.
//
// Mutation guard: assertions 1–3 fail if the mcp-servers.json write or
// SeedGuestMCPServers call is removed from the create / supervisor path.
//
// Run:
//
//	TMPDIR=/tmp NEXUS3_KERNEL_PATH=$(pwd)/images/kernel/vmlinux-x86_64 \
//	  NEXUS3_LIVE_REQUIRED=1 \
//	  go test -tags herdr_live ./internal/cli/ -run TestAgentSettingsMCPShareLive \
//	  -v -count=1
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

func TestAgentSettingsMCPShareLive(t *testing.T) {
	if os.Getenv("NEXUS3_LIVE_REQUIRED") == "" {
		t.Skip("set NEXUS3_LIVE_REQUIRED=1 to run live tests (requires KVM + built images)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skipf("mcp-share: /dev/kvm not available: %v", err)
	}
	if os.Getenv("NEXUS3_KERNEL_PATH") == "" {
		t.Skip("mcp-share: NEXUS3_KERNEL_PATH is not set; set it to a vmlinux image to run this test")
	}

	// Build the nexus3 binary (same pattern as agent_settings_share_live_test.go).
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "nexus3-mcpshare")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexus3")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("mcp-share: nexus3 binary cannot be built: %v\n%s", err, out)
	}

	image := os.Getenv("NEXUS3_SHARE_IMAGE")
	if image == "" {
		image = "nexus3-agent-base"
	}

	// --- Prepare fake HOME with fixture files. ---
	//
	// ~/.claude.json  — top-level mcpServers with nexus3-probe (stdio, uses ${PROBE_TOKEN}).
	// ~/.claude/CLAUDE.md, settings.json, skills/demo/SKILL.md — overlay marker content
	//   (MountAllowlist must be non-empty to trigger the A-MOUNT gate).
	// ~/.claude/.credentials.json — must be excluded by the allowlist (AC-4).
	fakeHome := t.TempDir()
	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir fake .claude: %v", err)
	}

	// ~/.claude.json: the authoritative MCP source that BuildSharedMCPServers reads.
	//
	// nexus3-probe is a NETWORK-FREE stdio server (command=cat) with a ${PROBE_TOKEN}
	// env var reference. The verbatim ref must appear in the guest; the resolved value
	// must appear in cred.env instead (D-PP-04 stdio relaxation).
	// Two servers: nexus3-probe (has env), noenv-probe (no env field at all).
	// The verbatim-passthrough fix must preserve the absence of "env" in noenv-probe —
	// re-marshal would emit "env":null which Claude Code rejects.
	claudeJSON := `{"mcpServers":{"nexus3-probe":{"type":"stdio","command":"cat","args":[],"env":{"PROBE_TOKEN":"${PROBE_TOKEN}"}},"noenv-probe":{"command":"cat","args":[]}}}`
	if err := os.WriteFile(filepath.Join(fakeHome, ".claude.json"), []byte(claudeJSON+"\n"), 0o600); err != nil {
		t.Fatalf("write fake .claude.json: %v", err)
	}

	// Overlay marker files — required so MountAllowlist is non-empty and the
	// A-MOUNT gate fires (same pattern as agent_settings_share_live_test.go).
	const sharedMarker = "# Shared CLAUDE.md marker — MCP share live test\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(sharedMarker), 0o644); err != nil {
		t.Fatalf("write fake CLAUDE.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{}`+"\n"), 0o600); err != nil {
		t.Fatalf("write fake settings.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(claudeDir, "skills", "demo"), 0o755); err != nil {
		t.Fatalf("mkdir skills/demo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "skills", "demo", "SKILL.md"), []byte("# demo skill\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	// .credentials.json: must be excluded by allowlist (AC-4).
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"),
		[]byte(`{"fake":"secret-must-not-appear-in-guest"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write fake .credentials.json: %v", err)
	}

	// --- Primary test: create --agent claude-code. ---
	handle := fmt.Sprintf("mcpsharetest/probe-%d", time.Now().UnixMilli())

	t.Cleanup(func() {
		rmOut, rmErr := shareCmd(binary, "rm", handle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", handle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", handle, rmOut)
		}
	})

	// Build create command: fake HOME so BuildSharedMCPServers reads fakeHome/.claude.json,
	// and inject PROBE_TOKEN into subprocess env so resolveMCPStdioPayload resolves it.
	//
	// CLAUDE_CONFIG_DIR must be stripped: ClaudeCodeProfile.ConfigDirEnvVar = "CLAUDE_CONFIG_DIR"
	// so claudeDotJSONPath checks CLAUDE_CONFIG_DIR first. If the operator has it set (e.g.
	// Claude Code set it to ~/.claude), claudeDotJSONPath would return
	// $CLAUDE_CONFIG_DIR/.claude.json (not fakeHome/.claude.json), and BuildSharedMCPServers
	// would find no servers → stdioPayload empty → PROBE_TOKEN missing from cred.env.
	// Stripping it forces the HOME fallback so the path is fakeHome/.claude.json. ✓
	createCmd := shareCmdFakeHome(binary, fakeHome,
		"create", handle,
		"--agent", "claude-code",
		"--image", image,
		"--no-builtin-gh",
	)
	// Filter ambient PROBE_TOKEN and CLAUDE_CONFIG_DIR; inject fixture values.
	filteredEnv := make([]string, 0, len(createCmd.Env)+1)
	for _, kv := range createCmd.Env {
		if strings.HasPrefix(kv, "PROBE_TOKEN=") || strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		filteredEnv = append(filteredEnv, kv)
	}
	createCmd.Env = append(filteredEnv, "PROBE_TOKEN=probe-secret-value")

	createOut, createErr := createCmd.CombinedOutput()
	if createErr != nil {
		t.Fatalf("nexus3 create: %v\n%s\n(check NEXUS3_KERNEL_PATH and that %q is cached)", createErr, createOut, image)
	}
	t.Logf("nexus3 create:\n%s", createOut)

	// --- In-guest verification script. ---
	//
	// Does NOT use set -e so that all assertions run independently and each
	// produces a PASS or FAIL line. Emits MCP_SHARE_TRACER_OK only after all
	// assertions have been exercised (regardless of individual results).
	const tracerToken = "MCP_SHARE_TRACER_OK"
	script := `
# AC-1: /root/.claude.json must contain nexus3-probe with command=cat
#        and env.PROBE_TOKEN == "${PROBE_TOKEN}" (verbatim ref, NOT the resolved value).
ac1_result=$(node -e '
const fs = require("fs");
let cfg = {};
try {
  cfg = JSON.parse(fs.readFileSync("/root/.claude.json", "utf8"));
} catch (e) {
  process.stdout.write("FAIL AC-1: cannot read /root/.claude.json: " + e.message + "\n");
  process.exit(0);
}
const srv = cfg.mcpServers && cfg.mcpServers["nexus3-probe"];
if (!srv) {
  process.stdout.write("FAIL AC-1: nexus3-probe absent from /root/.claude.json mcpServers; actual: " + JSON.stringify(cfg.mcpServers) + "\n");
  process.exit(0);
}
if (srv.command !== "cat") {
  process.stdout.write("FAIL AC-1: command is " + JSON.stringify(srv.command) + ", want \"cat\"\n");
  process.exit(0);
}
const envRef = srv.env && srv.env["PROBE_TOKEN"];
if (envRef !== "${PROBE_TOKEN}") {
  process.stdout.write("FAIL AC-1: PROBE_TOKEN env ref is " + JSON.stringify(envRef) + ", want \"${PROBE_TOKEN}\" (verbatim)\n");
  process.exit(0);
}
process.stdout.write("AC1_OK\n");
' 2>&1)
echo "$ac1_result"

# AC-2: /run/nexus3/cred.env must contain PROBE_TOKEN=probe-secret-value.
if grep -qF 'PROBE_TOKEN=probe-secret-value' /run/nexus3/cred.env 2>/dev/null; then
  echo "AC2_OK"
else
  echo "FAIL AC-2: PROBE_TOKEN=probe-secret-value absent from cred.env"
  echo "  cred.env keys: $(grep -oP '^[A-Z_]+(?==)' /run/nexus3/cred.env 2>/dev/null | tr '\n' ' ')"
fi

# AC-3: claude mcp list (login shell so cred.env is sourced) must list nexus3-probe and noenv-probe.
# Does not require PROBE_TOKEN in cred.env: listing reads /root/.claude.json, not the env.
mcp_list_out=$(bash -lc 'claude mcp list 2>&1' || true)
if echo "$mcp_list_out" | grep -q 'nexus3-probe'; then
  echo "AC3_OK"
else
  echo "FAIL AC-3: nexus3-probe not listed by claude mcp list"
  echo "  claude mcp list output: $mcp_list_out"
fi
if echo "$mcp_list_out" | grep -q 'noenv-probe'; then
  echo "AC3_NOENV_OK"
else
  echo "FAIL AC-3-noenv: noenv-probe not listed by claude mcp list"
  echo "  claude mcp list output: $mcp_list_out"
fi

# AC-4: .credentials.json MUST NOT be present in /root/.claude.
if [ -f /root/.claude/.credentials.json ]; then
  echo "FAIL AC-4: .credentials.json present in guest — allowlist did not exclude it"
else
  echo "AC4_OK"
fi

# AC-6: noenv-probe verbatim passthrough — must have no "env":null or "headers":null.
node -e '
const fs = require("fs");
let cfg = {};
try {
  cfg = JSON.parse(fs.readFileSync("/root/.claude.json", "utf8"));
} catch (e) {
  process.stdout.write("FAIL AC-6: cannot read /root/.claude.json: " + e.message + "\n");
  process.exit(0);
}
const srv = cfg.mcpServers && cfg.mcpServers["noenv-probe"];
if (!srv) {
  process.stdout.write("FAIL AC-6: noenv-probe absent from /root/.claude.json mcpServers\n");
  process.exit(0);
}
if ("env" in srv && srv.env === null) {
  process.stdout.write("FAIL AC-6: noenv-probe has env:null (re-marshal pollution)\n");
  process.exit(0);
}
if ("headers" in srv && srv.headers === null) {
  process.stdout.write("FAIL AC-6: noenv-probe has headers:null (re-marshal pollution)\n");
  process.exit(0);
}
process.stdout.write("AC6_OK\n");
' 2>&1

echo ` + tracerToken + `
`
	execOut, execErr := shareCmd(binary, "exec", handle, "--", "/bin/bash", "-c", script).CombinedOutput()
	t.Logf("exec output:\n%s", execOut)
	if execErr != nil {
		t.Fatalf("nexus3 exec: %v\n%s", execErr, execOut)
	}

	if !bytes.Contains(execOut, []byte(tracerToken)) {
		t.Errorf("tracer token %q absent — exec never completed\n%s", tracerToken, execOut)
	}
	if !bytes.Contains(execOut, []byte("AC1_OK")) {
		t.Errorf("AC-1 FAIL: nexus3-probe definition not merged into /root/.claude.json mcpServers\n%s", execOut)
	}
	if !bytes.Contains(execOut, []byte("AC2_OK")) {
		t.Errorf("AC-2 FAIL: PROBE_TOKEN=probe-secret-value absent from cred.env\n%s", execOut)
	}
	if !bytes.Contains(execOut, []byte("AC3_OK")) {
		t.Errorf("AC-3 FAIL: nexus3-probe not listed by claude mcp list\n%s", execOut)
	}
	if !bytes.Contains(execOut, []byte("AC3_NOENV_OK")) {
		t.Errorf("AC-3-noenv FAIL: noenv-probe not listed by claude mcp list\n%s", execOut)
	}
	if !bytes.Contains(execOut, []byte("AC4_OK")) {
		t.Errorf("AC-4 FAIL: .credentials.json present in guest\n%s", execOut)
	}
	if !bytes.Contains(execOut, []byte("AC6_OK")) {
		t.Errorf("AC-6 FAIL: noenv-probe verbatim passthrough corrupted (env:null or headers:null present)\n%s", execOut)
	}

	// --- AC-5: --no-share-settings prevents MCP injection. ---
	noShareHandle := fmt.Sprintf("mcpsharetest/noshare-%d", time.Now().UnixMilli())
	t.Cleanup(func() {
		rmOut, rmErr := shareCmd(binary, "rm", noShareHandle).CombinedOutput()
		if rmErr != nil {
			t.Logf("cleanup: nexus3 rm %s: %v\n%s", noShareHandle, rmErr, rmOut)
		} else {
			t.Logf("cleanup: nexus3 rm %s: %s", noShareHandle, rmOut)
		}
	})

	noShareCmd := shareCmdFakeHome(binary, fakeHome,
		"create", noShareHandle,
		"--agent", "claude-code",
		"--image", image,
		"--no-builtin-gh",
		"--no-share-settings",
	)
	// Same env cleanup as positive leg: strip CLAUDE_CONFIG_DIR + inject PROBE_TOKEN.
	// This ensures --no-share-settings (not absent vars) is what prevents injection.
	filteredEnv2 := make([]string, 0, len(noShareCmd.Env)+1)
	for _, kv := range noShareCmd.Env {
		if strings.HasPrefix(kv, "PROBE_TOKEN=") || strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		filteredEnv2 = append(filteredEnv2, kv)
	}
	noShareCmd.Env = append(filteredEnv2, "PROBE_TOKEN=probe-secret-value")

	noShareOut, noShareErr := noShareCmd.CombinedOutput()
	if noShareErr != nil {
		t.Fatalf("nexus3 create (no-share): %v\n%s", noShareErr, noShareOut)
	}
	t.Logf("nexus3 create (no-share):\n%s", noShareOut)

	checkNoMCP := `set -euo pipefail
node -e '
let cfg = {};
try { cfg = JSON.parse(require("fs").readFileSync("/root/.claude.json","utf8")); } catch(e) {}
if (cfg.mcpServers && cfg.mcpServers["nexus3-probe"]) {
  process.stderr.write("FAIL AC-5: nexus3-probe present in --no-share-settings sandbox\n");
  process.exit(1);
}
console.log("AC5_NO_MCP_OK");
'
`
	nsOut, nsErr := shareCmd(binary, "exec", noShareHandle, "--", "/bin/bash", "-c", checkNoMCP).CombinedOutput()
	t.Logf("no-share exec: %s", nsOut)
	if nsErr != nil {
		t.Fatalf("nexus3 exec (no-share): %v\n%s", nsErr, nsOut)
	}
	if !bytes.Contains(nsOut, []byte("AC5_NO_MCP_OK")) {
		t.Errorf("--no-share-settings sandbox unexpectedly has nexus3-probe in mcpServers\n%s", nsOut)
	}
}

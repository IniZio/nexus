package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestSeedGuestAgentOnboarding_MCPServersIncluded is the mutation guard for
// the node-free host-side MCP merge (D-MCP-HOST).
//
// It verifies that mcpServers passed to SeedGuestAgentOnboarding appear in the
// written /root/.claude.json without any in-guest toolchain (no node, no jq,
// no python). The spy executes the actual shell script on the host /bin/sh.
//
// RED/GREEN contract:
//
//	RED  — remove cfg.MCPServers = servers from SeedGuestAgentOnboarding, or
//	        remove the MCPServers field from claudeOnboardingConfig → mcpServers
//	        key absent from the output file → test fails at
//	        "mcpServers key absent".
//	GREEN — host-side merge in place → mcpServers present, placeholder
//	        Authorization value preserved verbatim.
//
// This closes the gap that the previous stub-execer suite missed: the stubs
// returned success while the real node script exited 127 in the guest.
func TestSeedGuestAgentOnboarding_MCPServersIncluded(t *testing.T) {
	dir := t.TempDir()
	spy, rec := newSpyExecer(t, dir)

	servers := map[string]json.RawMessage{
		"linear-server": json.RawMessage(`{"type":"http","url":"https://mcp.linear.app/sse","headers":{"Authorization":"${NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION}"}}`),
	}

	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", servers, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding: %v", err)
	}
	if !rec.Called {
		t.Fatal("execer was not called")
	}

	outPath := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, data)
	}

	mcpRaw, ok := cfg["mcpServers"]
	if !ok {
		t.Fatal("mcpServers key absent from /root/.claude.json — host-side MCP merge not applied " +
			"(D-MCP-HOST RED: reproduce by removing cfg.MCPServers = servers from SeedGuestAgentOnboarding)")
	}
	mcp, ok := mcpRaw.(map[string]any)
	if !ok {
		t.Fatalf("mcpServers is not an object: %T %v", mcpRaw, mcpRaw)
	}
	if _, ok := mcp["linear-server"]; !ok {
		t.Errorf("mcpServers[\"linear-server\"] missing; got keys: %v", mcp)
	}

	// Authorization placeholder must be preserved verbatim: the MITM refresher
	// swaps it at request time; inlining a real token here would be a security
	// defect.
	raw, _ := json.Marshal(mcp["linear-server"])
	if !strings.Contains(string(raw), "${NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION}") {
		t.Errorf("Authorization placeholder not preserved verbatim in merged output: %s", raw)
	}
}

// TestSeedGuestAgentOnboarding_NilServersOmitsMCPKey verifies that when no
// MCP servers are provided the mcpServers key is absent (omitempty behaviour).
func TestSeedGuestAgentOnboarding_NilServersOmitsMCPKey(t *testing.T) {
	dir := t.TempDir()
	spy, _ := newSpyExecer(t, dir)

	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", nil, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding(nil servers): %v", err)
	}

	outPath := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, data)
	}
	if _, ok := cfg["mcpServers"]; ok {
		t.Errorf("mcpServers key must be absent when servers is nil (omitempty); got: %v", cfg["mcpServers"])
	}
}

// newBypassConsentExecer returns a GuestExecer that runs GuestBypassConsentScript
// on the host /bin/sh, redirecting /root/.claude to captureDir so the test
// can inspect the written settings.json without a live VM.
func newBypassConsentExecer(t *testing.T, captureDir string) GuestExecer {
	t.Helper()
	return func(ctx context.Context, _ domain.SandboxID, argv []string, stdin io.Reader) (int32, error) {
		if len(argv) < 3 || argv[0] != "/bin/sh" || argv[1] != "-c" {
			t.Errorf("newBypassConsentExecer: expected [/bin/sh -c <script>], got %v", argv)
			return 1, nil
		}
		script := strings.ReplaceAll(argv[2], "/root/.claude", captureDir)
		var stdinBytes []byte
		if stdin != nil {
			var err error
			stdinBytes, err = io.ReadAll(stdin)
			if err != nil {
				return 1, err
			}
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		cmd.Stdin = bytes.NewReader(stdinBytes)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return int32(ee.ExitCode()), nil
			}
			return 1, err
		}
		return 0, nil
	}
}

// TestSeedGuestBypassConsent_NoNodeRequired verifies that SeedGuestBypassConsent
// writes a valid settings.json containing skipDangerousModePermissionPrompt:true
// using only POSIX sh (no node). This proves the fix for the 127 exit that the
// original node-based script produced.
func TestSeedGuestBypassConsent_NoNodeRequired(t *testing.T) {
	dir := t.TempDir()
	spy := newBypassConsentExecer(t, dir)

	var id domain.SandboxID
	if err := SeedGuestBypassConsent(context.Background(), id, spy); err != nil {
		t.Fatalf("SeedGuestBypassConsent: %v", err)
	}

	settingsPath := filepath.Join(dir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\nraw: %s", err, data)
	}
	if v, ok := cfg["skipDangerousModePermissionPrompt"]; !ok || v != true {
		t.Errorf("skipDangerousModePermissionPrompt missing or not true; got: %v", cfg)
	}
}

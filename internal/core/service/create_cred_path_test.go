package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// TestDedicatedCredStorePathForProfile_ClaudeCodeLegacyPath is the primary
// regression guard for live operator credentials. claude-code must resolve to
// exactly ~/.config/nexus3/creds.json. Any change to this literal silently
// invalidates every existing operator sandbox — do not adjust the assertion.
//
// MUTATION PROOF: make DedicatedCredStorePathForProfile ignore the profile
// name and always return the per-agent path ("agent-creds/claude-code.json")
// → this test goes RED. Restore the claude-code special-case → GREEN.
func TestDedicatedCredStorePathForProfile_ClaudeCodeLegacyPath(t *testing.T) {
	// Ensure the env-var override is absent so we test the default derivation.
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".config", "nexus3", "creds.json")

	got := service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile)
	if got != want {
		t.Errorf("claude-code store path = %q; want exactly %q\n"+
			"REGRESSION: changing this path silently logs operators out of every existing sandbox.",
			got, want)
	}
}

// TestDedicatedCredStorePathForProfile_DistinctPaths proves AC-1: two distinct
// profiles produce two distinct store paths. The test fails if
// DedicatedCredStorePathForProfile ignores the agent argument.
func TestDedicatedCredStorePathForProfile_DistinctPaths(t *testing.T) {
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "")

	claudePath := service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile)
	cursorPath := service.DedicatedCredStorePathForProfile(cred.CursorAgentProfile)

	if claudePath == cursorPath {
		t.Errorf("claude-code and cursor-agent resolved to the same store path %q; "+
			"two distinct agents must have distinct stores to avoid credential collision", claudePath)
	}
}

// TestDedicatedLockFilePathForProfile_DistinctPaths proves AC-2: two distinct
// profiles produce two distinct lockfile paths. A shared lock would serialise
// unrelated agents and give false cross-agent safety.
func TestDedicatedLockFilePathForProfile_DistinctPaths(t *testing.T) {
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "")

	claudeLock := service.DedicatedLockFilePathForProfile(cred.ClaudeCodeProfile)
	cursorLock := service.DedicatedLockFilePathForProfile(cred.CursorAgentProfile)

	if claudeLock == cursorLock {
		t.Errorf("claude-code and cursor-agent resolved to the same lockfile path %q; "+
			"a shared lock gives false safety across unrelated agents", claudeLock)
	}
}

// TestDedicatedCredStorePathForProfile_ClaudeCodeEnvOverride verifies that
// NEXUS3_DEDICATED_CRED_STORE still overrides the claude-code path (backward
// compatibility for users who pin a custom path).
func TestDedicatedCredStorePathForProfile_ClaudeCodeEnvOverride(t *testing.T) {
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "/custom/creds.json")

	got := service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile)
	if got != "/custom/creds.json" {
		t.Errorf("NEXUS3_DEDICATED_CRED_STORE override ignored: got %q, want /custom/creds.json", got)
	}
}

// TestDedicatedCredStorePathForProfile_CursorLayout verifies the on-disk layout
// for cursor: ~/.config/nexus3/agent-creds/cursor.json.
func TestDedicatedCredStorePathForProfile_CursorLayout(t *testing.T) {
	t.Setenv("NEXUS3_DEDICATED_CRED_STORE", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".config", "nexus3", "agent-creds", "cursor.json")

	got := service.DedicatedCredStorePathForProfile(cred.CursorAgentProfile)
	if got != want {
		t.Errorf("cursor store path = %q; want %q", got, want)
	}
}

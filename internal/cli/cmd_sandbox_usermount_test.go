package cli

import (
	"testing"
)

// TestParseSandboxCreateArgs_NoUserMountsFlag verifies --no-user-mounts sets
// the noUserMounts field. Mutation guard: if the case is removed from the
// switch, the flag is silently ignored and the field stays false.
func TestParseSandboxCreateArgs_NoUserMountsFlag(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"proj/name", "--no-user-mounts"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if !f.noUserMounts {
		t.Error("expected noUserMounts=true when --no-user-mounts is passed")
	}
}

// TestParseSandboxCreateArgs_NoUserMountsDefault verifies the flag defaults to
// false (user-mounts ON by default).
func TestParseSandboxCreateArgs_NoUserMountsDefault(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"proj/name"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.noUserMounts {
		t.Error("expected noUserMounts=false by default")
	}
}

// TestParseSandboxCreateArgs_NoUserMounts_IndependentOfNoShareSettings verifies
// --no-user-mounts and --no-share-settings are independent flags.
func TestParseSandboxCreateArgs_NoUserMounts_IndependentOfNoShareSettings(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"proj/name", "--no-share-settings"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if !f.noShareSettings {
		t.Error("expected noShareSettings=true")
	}
	if f.noUserMounts {
		t.Error("expected noUserMounts=false when only --no-share-settings passed")
	}
}

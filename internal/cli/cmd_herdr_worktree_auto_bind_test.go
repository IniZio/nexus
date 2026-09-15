package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHerdrWorktreeAutoBindDecision pins the --auto predicate: nexus3-
// onboarded checkout binds even with no sibling workspace (FRICTION-1).
// MUTATION PROOF: drop hasConfig arm → "config only" RED; force true → "neither" RED.
func TestHerdrWorktreeAutoBindDecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		repoBound bool
		hasConfig bool
		wantBind  bool
	}{
		{"neither", false, false, false},
		{"config only (first worktree of an onboarded repo)", false, true, true},
		{"sibling bound only", true, false, true},
		{"both", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bind, reason := herdrWorktreeAutoBindDecision(tc.repoBound, tc.hasConfig)
			if bind != tc.wantBind {
				t.Fatalf("bind = %v, want %v", bind, tc.wantBind)
			}
			if reason == "" {
				t.Fatal("reason must never be empty: the skip path prints it")
			}
		})
	}
}

func TestHerdrRepoHasNexus3Config(t *testing.T) {
	t.Run("empty dir", func(t *testing.T) {
		if herdrRepoHasNexus3Config(t.TempDir()) {
			t.Fatal("bare checkout reported as onboarded")
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if herdrRepoHasNexus3Config("") {
			t.Fatal("empty path reported as onboarded")
		}
	})
	t.Run("nexus3.yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte("image: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !herdrRepoHasNexus3Config(dir) {
			t.Fatal("nexus3.yaml not detected")
		}
	})
	t.Run(".nexus/Containerfile", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".nexus"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".nexus", "Containerfile"), []byte("FROM x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !herdrRepoHasNexus3Config(dir) {
			t.Fatal(".nexus/Containerfile not detected")
		}
	})
	t.Run("nexus3.yaml as directory is not config", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "nexus3.yaml"), 0o755); err != nil {
			t.Fatal(err)
		}
		if herdrRepoHasNexus3Config(dir) {
			t.Fatal("directory named nexus3.yaml reported as config")
		}
	})
}

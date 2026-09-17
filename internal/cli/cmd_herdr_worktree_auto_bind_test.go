package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
)

// TestHerdrWorktreeAutoBindDecision pins the --auto predicate: nexus-
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

func TestHerdrRepoHasNexusConfig(t *testing.T) {
	t.Run("empty dir", func(t *testing.T) {
		if herdrRepoHasNexusConfig(t.TempDir()) {
			t.Fatal("bare checkout reported as onboarded")
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if herdrRepoHasNexusConfig("") {
			t.Fatal("empty path reported as onboarded")
		}
	})
	t.Run(".nexus/config.yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".nexus"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.ConfigRelPath), []byte("image: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !herdrRepoHasNexusConfig(dir) {
			t.Fatal(".nexus/config.yaml not detected")
		}
	})
	t.Run("root-level legacy config file only is NOT config (hard cutover)", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "nexus"+".yaml"), []byte("image: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if herdrRepoHasNexusConfig(dir) {
			t.Fatal("legacy root-level config reported as onboarded")
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
		if !herdrRepoHasNexusConfig(dir) {
			t.Fatal(".nexus/Containerfile not detected")
		}
	})
	t.Run(".nexus/config.yaml as directory is not config", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, config.ConfigRelPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if herdrRepoHasNexusConfig(dir) {
			t.Fatal("directory named .nexus/config.yaml reported as config")
		}
	})
}

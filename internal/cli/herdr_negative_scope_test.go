package cli

// herdr_negative_scope_test.go — mechanical guard that the herdr ↔ nexus3
// integration stays within agreed scope. Assertions are intentionally brittle:
// scope expansion silently triggers failure and forces review.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the repository root by walking up to go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(callerFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (no go.mod found)")
		}
		dir = parent
	}
}

// TestNegativeScope_HerdrPluginTomlTablesOnly asserts permitted tables only.
func TestNegativeScope_HerdrPluginTomlTablesOnly(t *testing.T) {
	root := repoRoot(t)
	tomlPath := filepath.Join(root, "plugins", "herdr", "herdr-plugin.toml")

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read %s: %v", tomlPath, err)
	}
	content := string(data)

	// Permitted tables: [[build]], [[panes]], [[actions]], [[events]], [[startup]].
	// [[events]] added D-HSH-20 (registry uses dots not underscores: worktree.removed).
	// Hook stops removed worktree leaking sandbox. [[startup]] from port-forward motive:
	// herdr ≥0.9.0 runs command on laptop to manage host-side port forwards. Guard stays
	// narrow: new tables require explicit decision. Event NAMES validated separately by
	// TestHerdrPluginManifest_EventNamesValid.
	permitted := []string{"[[build]]", "[[panes]]", "[[actions]]", "[[events]]", "[[startup]]"}

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[[") {
			continue
		}
		if idx := strings.Index(trimmed, "#"); idx >= 0 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}
		allowed := false
		for _, p := range permitted {
			if trimmed == p {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("unexpected TOML table header in herdr-plugin.toml: %q (only %v are permitted)",
				trimmed, permitted)
		}
	}
}

// TestNegativeScope_NoFUSEReferences asserts no "fuse" in plugins/herdr/.
func TestNegativeScope_NoFUSEReferences(t *testing.T) {
	root := repoRoot(t)

	var targets []string

	herdrPluginDir := filepath.Join(root, "plugins", "herdr")
	if err := filepath.WalkDir(herdrPluginDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			targets = append(targets, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", herdrPluginDir, err)
	}

	targets = append(targets, filepath.Join(root, "internal", "cli", "cmd_herdr_space.go"))

	for _, path := range targets {
		data, err := os.ReadFile(path)
		if err != nil {
			// Non-fatal: the file may not exist yet (other slices in flight).
			t.Logf("skip (not readable): %s: %v", path, err)
			continue
		}
		if strings.Contains(strings.ToLower(string(data)), "fuse") {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("FUSE reference found in %s — FUSE is out of scope for this integration", rel)
		}
	}
}

// TestNegativeScope_NoHerdrVendorOrForkDir asserts no vendor/fork
// under plugins/herdr/. Permitted: "abi", "bin", top-level files.
func TestNegativeScope_NoHerdrVendorOrForkDir(t *testing.T) {
	root := repoRoot(t)
	herdrPluginDir := filepath.Join(root, "plugins", "herdr")

	permittedDirs := map[string]bool{
		"abi": true,
		"bin": true,
	}

	entries, err := os.ReadDir(herdrPluginDir)
	if err != nil {
		t.Fatalf("read dir %s: %v", herdrPluginDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !permittedDirs[entry.Name()] {
			rel := filepath.Join("plugins", "herdr", entry.Name())
			t.Errorf("unexpected directory %s — a herdr vendor/fork/patch directory is out of scope", rel)
		}
	}
}

package cli

// herdr_negative_scope_test.go — mechanical guard that the herdr ↔ nexus3
// integration stays within its agreed scope:
//
//   1. herdr-plugin.toml uses ONLY [[build]], [[panes]], [[actions]] tables.
//   2. No FUSE/fuse references exist under plugins/herdr/ or in cmd_herdr_space.go.
//   3. No vendor/fork patch directory was introduced under plugins/herdr/.
//
// These assertions are intentionally brittle: a future change that would
// silently expand the integration scope causes the test to fail and forces an
// explicit review.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot walks up from the calling file's directory until it finds a go.mod,
// returning that directory as the repository root.
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

// TestNegativeScope_HerdrPluginTomlTablesOnly asserts that herdr-plugin.toml
// contains no [[workspace_provider]] or other unexpected contribution tables —
// only [[build]], [[panes]], and [[actions]] are permitted.
func TestNegativeScope_HerdrPluginTomlTablesOnly(t *testing.T) {
	root := repoRoot(t)
	tomlPath := filepath.Join(root, "plugins", "herdr", "herdr-plugin.toml")

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read %s: %v", tomlPath, err)
	}
	content := string(data)

	// Permitted table headers (the full set agreed in the spec).
	permitted := []string{"[[build]]", "[[panes]]", "[[actions]]"}

	// Collect all [[...]] table headers present in the file.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[[") {
			continue
		}
		// Strip inline comments.
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

// TestNegativeScope_NoFUSEReferences asserts that the string "fuse" (case-
// insensitive) does not appear anywhere under plugins/herdr/ or in
// internal/cli/cmd_herdr_space.go.
func TestNegativeScope_NoFUSEReferences(t *testing.T) {
	root := repoRoot(t)

	var targets []string

	// Walk plugins/herdr/ recursively.
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

	// Add cmd_herdr_space.go.
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

// TestNegativeScope_NoHerdrVendorOrForkDir asserts that no vendor/fork patch
// directory exists under plugins/herdr/.  Permitted subdirectories are:
// "abi", "bin" (and any files at the top level).
func TestNegativeScope_NoHerdrVendorOrForkDir(t *testing.T) {
	root := repoRoot(t)
	herdrPluginDir := filepath.Join(root, "plugins", "herdr")

	// Permitted subdirectory names (all others are unexpected).
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

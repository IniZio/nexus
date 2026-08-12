package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveContainerfilePath verifies the three-tier Containerfile resolution
// used by the --file builder-VM path (G7).
//
//  1. Explicit override: returned as-is, no stat required.
//  2. Default .nexus/Containerfile: found when present.
//  3. Fallback .nexus/Dockerfile:   used when Containerfile absent.
//  4. Error: neither path exists and no explicit override given.
func TestResolveContainerfilePath(t *testing.T) {
	t.Run("explicit_override_returned_as_is", func(t *testing.T) {
		got, err := resolveContainerfilePath("/any/workspace", "/explicit/Custom.containerfile")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/explicit/Custom.containerfile" {
			t.Errorf("got %q, want %q", got, "/explicit/Custom.containerfile")
		}
	})

	t.Run("containerfile_preferred_over_dockerfile", func(t *testing.T) {
		ws := t.TempDir()
		nexusDir := filepath.Join(ws, ".nexus")
		if err := os.Mkdir(nexusDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Both files present; Containerfile wins.
		must(t, os.WriteFile(filepath.Join(nexusDir, "Containerfile"), []byte("FROM scratch"), 0o644))
		must(t, os.WriteFile(filepath.Join(nexusDir, "Dockerfile"), []byte("FROM scratch"), 0o644))

		got, err := resolveContainerfilePath(ws, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(nexusDir, "Containerfile")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("dockerfile_fallback_when_containerfile_absent", func(t *testing.T) {
		ws := t.TempDir()
		nexusDir := filepath.Join(ws, ".nexus")
		if err := os.Mkdir(nexusDir, 0o755); err != nil {
			t.Fatal(err)
		}
		must(t, os.WriteFile(filepath.Join(nexusDir, "Dockerfile"), []byte("FROM scratch"), 0o644))

		got, err := resolveContainerfilePath(ws, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Join(nexusDir, "Dockerfile")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("error_when_neither_exists", func(t *testing.T) {
		ws := t.TempDir()
		_, err := resolveContainerfilePath(ws, "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

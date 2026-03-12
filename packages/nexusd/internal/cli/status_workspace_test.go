package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusCommandHandlesBoulderStateLoadError(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	originalJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = originalJSONOutput })

	boulderPath := filepath.Join(homeDir, ".nexus", "boulder")
	if err := os.MkdirAll(filepath.Join(homeDir, ".nexus"), 0755); err != nil {
		t.Fatalf("failed to create .nexus dir: %v", err)
	}
	if err := os.WriteFile(boulderPath, []byte("not-a-directory"), 0644); err != nil {
		t.Fatalf("failed to create conflicting boulder path: %v", err)
	}

	var panicked any
	output := captureStdout(t, func() {
		defer func() {
			panicked = recover()
		}()
		statusCmd.Run(statusCmd, nil)
	})

	if panicked != nil {
		t.Fatalf("status command panicked when boulder state failed to load: %v", panicked)
	}

	if !strings.Contains(output, "Boulder: unknown") {
		t.Fatalf("expected fallback boulder output, got %q", output)
	}
}

func TestSetActiveWorkspaceClearIsIdempotent(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	if err := setActiveWorkspace(""); err != nil {
		t.Fatalf("expected clearing unset active workspace to succeed, got %v", err)
	}
}

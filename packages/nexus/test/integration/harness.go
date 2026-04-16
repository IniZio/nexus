//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// DriverConfig identifies a driver configuration under test.
type DriverConfig struct {
	Backend    string // "firecracker", "lima", "process"
	Mode       string // "dedicated", "pool", "process"
	SkipUnless func(t *testing.T)
}

// AllDrivers enumerates all 5 driver configurations with their hardware guards.
var AllDrivers = []DriverConfig{
	{
		Backend: "firecracker",
		Mode:    "dedicated",
		SkipUnless: func(t *testing.T) {
			t.Helper()
			if _, err := os.Stat("/dev/kvm"); err != nil {
				t.Skip("requires KVM (/dev/kvm not present)")
			}
		},
	},
	{
		Backend: "firecracker",
		Mode:    "pool",
		SkipUnless: func(t *testing.T) {
			t.Helper()
			if _, err := os.Stat("/dev/kvm"); err != nil {
				t.Skip("requires KVM (/dev/kvm not present)")
			}
		},
	},
	{
		Backend: "lima",
		Mode:    "dedicated",
		SkipUnless: func(t *testing.T) {
			t.Helper()
			if !isRunningOnMacOS() {
				t.Skip("requires macOS")
			}
			out, _ := exec.Command("sysctl", "-n", "kern.hv_support").Output()
			if strings.TrimSpace(string(out)) != "1" {
				t.Skip("requires nested virtualization (kern.hv_support=1)")
			}
		},
	},
	{
		Backend: "lima",
		Mode:    "pool",
		SkipUnless: func(t *testing.T) {
			t.Helper()
			if !isRunningOnMacOS() {
				t.Skip("requires macOS")
			}
		},
	},
	{
		Backend: "process",
		Mode:    "process",
		SkipUnless: func(t *testing.T) {
			t.Helper()
			// process/sandbox runs everywhere, never skip
		},
	},
}

// WorkspaceHandle is a reference to a created workspace for test use.
type WorkspaceHandle struct {
	ID      string
	Backend string
	Mode    string
}

// CreateWorkspace creates a workspace using the nexus CLI and returns a handle.
func CreateWorkspace(t *testing.T, cfg DriverConfig, projectRoot string) WorkspaceHandle {
	t.Helper()
	args := []string{"sandbox", "create", "--fresh", "--backend", cfg.Backend, "--repo", projectRoot}
	cmd := exec.Command("nexus", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create workspace: %v\n%s", err, out)
	}
	id := parseWorkspaceID(string(out))
	if id == "" {
		t.Fatalf("could not parse workspace ID from output: %q", string(out))
	}
	t.Cleanup(func() { DestroyWorkspace(t, id) })
	return WorkspaceHandle{ID: id, Backend: cfg.Backend, Mode: cfg.Mode}
}

// ExecInWorkspace runs a shell command inside the workspace and returns stdout.
func ExecInWorkspace(t *testing.T, ws WorkspaceHandle, shellCmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nexus", "sandbox", "exec", ws.ID, "--", "sh", "-c", shellCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exec in workspace %s: %v\n%s", ws.ID, err, out)
	}
	return string(out)
}

// DestroyWorkspace destroys a workspace by ID, ignoring errors (used in cleanup).
func DestroyWorkspace(t *testing.T, id string) {
	t.Helper()
	exec.Command("nexus", "sandbox", "remove", "-y", id).Run() //nolint:errcheck
}

// ForkWorkspace forks a workspace and returns the child handle.
func ForkWorkspace(t *testing.T, parent WorkspaceHandle) WorkspaceHandle {
	t.Helper()
	childName := fmt.Sprintf("fork-%d", time.Now().UnixNano())
	cmd := exec.Command("nexus", "sandbox", "fork", parent.ID, childName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fork workspace %s: %v\n%s", parent.ID, err, out)
	}
	childID := parseWorkspaceID(string(out))
	if childID == "" {
		t.Fatalf("could not parse child workspace ID from fork output: %q", string(out))
	}
	t.Cleanup(func() { DestroyWorkspace(t, childID) })
	return WorkspaceHandle{ID: childID, Backend: parent.Backend, Mode: parent.Mode}
}

// idPattern matches "(id: <uuid-or-id>)" in CLI output lines.
var idPattern = regexp.MustCompile(`\(id:\s*([^\s)]+)\)`)

func parseWorkspaceID(output string) string {
	if m := idPattern.FindStringSubmatch(output); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func isRunningOnMacOS() bool {
	return os.Getenv("RUNNER_OS") == "macOS" || os.Getenv("GOOS") == "darwin"
}

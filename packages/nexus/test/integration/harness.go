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

// ansiEscape strips ANSI terminal escape sequences from s.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][AB012]`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

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

	// Workspace is created in StateCreated; must be started before exec.
	startOut, startErr := exec.Command("nexus", "sandbox", "start", id).CombinedOutput()
	if startErr != nil {
		t.Fatalf("start workspace %s: %v\n%s", id, startErr, startOut)
	}

	return WorkspaceHandle{ID: id, Backend: cfg.Backend, Mode: cfg.Mode}
}

// ExecInWorkspace runs a shell command inside the workspace and returns stdout.
// It wraps the command with sentinel markers to extract output from PTY noise.
func ExecInWorkspace(t *testing.T, ws WorkspaceHandle, shellCmd string) string {
	t.Helper()
	const begin = "__NEXUS_BEGIN__"
	const end = "__NEXUS_END__"
	wrapped := fmt.Sprintf("echo %s; %s; echo %s", begin, shellCmd, end)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nexus", "sandbox", "exec", ws.ID, "--", "sh", "-c", wrapped)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exec in workspace %s: %v\n%s", ws.ID, err, out)
	}
	clean := stripANSI(strings.ReplaceAll(string(out), "\r\n", "\n"))
	// Extract lines between sentinels.
	// We may see the sentinel twice: once echoed as part of the typed command,
	// and once printed by the echo command itself. Find the last occurrence of
	// begin (i.e. the actual output line) by scanning from the end.
	endIdx := strings.LastIndex(clean, end)
	// Find the last begin that comes before endIdx.
	startIdx := strings.LastIndex(clean[:endIdx], begin)
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return strings.TrimSpace(clean)
	}
	inner := clean[startIdx+len(begin) : endIdx]
	return strings.TrimSpace(inner)
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

	startOut, startErr := exec.Command("nexus", "sandbox", "start", childID).CombinedOutput()
	if startErr != nil {
		t.Fatalf("start forked workspace %s: %v\n%s", childID, startErr, startOut)
	}

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

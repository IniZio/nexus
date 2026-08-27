// TBD-PD-40: DEFAULT-CI binary-boundary test for the __herdr-plugin verb.
//
// Problem: every other __herdr-plugin test is behind //go:build herdr_live,
// which CI skips. Deleting the __herdr-plugin verb registration in
// cmd_herdr_plugin.go would NOT fail plain `go test ./...` — exactly the
// dead-feature shape of the historic --agent-egress defect.
//
// This file closes that gap: it builds the real nexus3 binary (no build tags
// required) and drives it through the __herdr-plugin argv boundary using the
// `abi` subverb, which is fully hermetic — no KVM, no VM, no sandbox service,
// no filesystem writes. The `abi` case simply prints herdrPluginABIVersion and
// returns nil, making it runnable in any CI environment.
//
// Mutation invariant (verified by the author, see test comment):
//   - verb REGISTERED  → exit 0, stdout "2\n"
//   - verb ABSENT      → exit 2, stderr contains "unknown command"
//
// The test distinguishes these two outcomes, so removing the Register(…)
// block for __herdr-plugin makes the test RED.
package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHerdrPluginABI_binaryBoundary compiles the real nexus3 binary and
// invokes it as `nexus3 __herdr-plugin abi`. It asserts:
//  1. The process exits 0 (verb is registered and dispatch succeeds).
//  2. Stdout is the ABI version string "2" (the hermetic abi subverb ran).
//
// Deleting the Register(Command{Name: "__herdr-plugin", …}) block in
// cmd_herdr_plugin.go causes the binary to print "unknown command:
// __herdr-plugin" and exit 2 — making this test RED without any tag required.
//
// The abi subverb is chosen because it is the only subverb of __herdr-plugin
// that requires no sandbox service, no filesystem state, no KVM, and no herdr
// daemon: it is a single fmt.Fprintln call followed by return nil.
func TestHerdrPluginABI_binaryBoundary(t *testing.T) {
	t.Helper()

	// Build the real nexus3 binary into a temp directory.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "nexus3")

	build := exec.Command("go", "build", "-o", binPath, "./cmd/nexus3")
	build.Dir = filepath.Join(moduleRoot(t), "") // repo root
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/nexus3 failed:\n%s", out)
	}

	// Invoke: nexus3 __herdr-plugin abi
	// This is the sharpest hermetic probe:
	//   - verb registered   → exit 0, stdout == "2\n"
	//   - verb absent       → exit 2, stderr contains "unknown command: __herdr-plugin"
	cmd := exec.Command(binPath, "__herdr-plugin", "abi")
	// Provide a clean environment; XDG_STATE_HOME isolation keeps the binary
	// from touching the operator's real state, matching testmain_test.go policy.
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+binDir)

	stdout, stderr, exitCode := runCapture(t, cmd)

	if exitCode != 0 {
		// Distinguish "verb absent" from other failures so the mutation
		// error message is actionable.
		if strings.Contains(stderr, "unknown command") {
			t.Fatalf("__herdr-plugin verb is NOT registered in the binary.\n"+
				"  exit=%d stderr=%q\n"+
				"  Fix: ensure Register(Command{Name: \"__herdr-plugin\", …}) "+
				"exists in cmd_herdr_plugin.go", exitCode, stderr)
		}
		t.Fatalf("nexus3 __herdr-plugin abi exited %d\n  stdout=%q stderr=%q",
			exitCode, stdout, stderr)
	}

	got := strings.TrimSpace(stdout)
	if got != herdrPluginABIVersion {
		t.Fatalf("__herdr-plugin abi: want ABI version %q, got %q (stdout=%q)",
			herdrPluginABIVersion, got, stdout)
	}
}

// moduleRoot walks up from the package directory to find go.mod, returning
// the repo root. Required because exec.Command needs an absolute repo root
// to run `go build ./cmd/nexus3`.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("moduleRoot: go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// runCapture runs cmd and returns (stdout, stderr, exitCode) without failing
// the test on non-zero exit — the caller decides what constitutes failure.
func runCapture(t *testing.T, cmd *exec.Cmd) (stdout, stderr string, exitCode int) {
	t.Helper()
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("exec: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

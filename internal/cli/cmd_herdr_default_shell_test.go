package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// fakeDefaultShellGetter implements sandboxGetter for tests.
type fakeDefaultShellGetter struct {
	sb  domain.Sandbox
	err error
}

func (f *fakeDefaultShellGetter) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return f.sb, f.err
}

// fakeDialableGetter extends fakeDefaultShellGetter with DialGuest support.
// Used to test the substrate-dialability check added for the CRITICAL gap where
// "State == Running" is insufficient — the substrate/driver may be unreachable.
//
// dialedRef and dialedPort record the exact arguments passed to DialGuest so
// tests can assert IDENTITY (right sandbox, right port), not just EXISTENCE.
// Mutation proof: changing the ref or port in herdrDefaultShellCore to wrong
// values still dials and "succeeds" — without recording the args the fake
// discards them and the test stays green. With them the assertion fails RED.
type fakeDialableGetter struct {
	fakeDefaultShellGetter
	dialErr    error
	dialed     bool
	dialedRef  string
	dialedPort uint32
}

func (f *fakeDialableGetter) DialGuest(_ context.Context, ref string, port uint32) (net.Conn, error) {
	f.dialed = true
	f.dialedRef = ref
	f.dialedPort = port
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	// Return a connected pipe for the success path; caller closes it.
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

// capturedExec records the most recent execFn call. It never actually replaces
// the process, so the test continues normally after the call.
type capturedExec struct {
	argv0 string
	argv  []string
	envv  []string
	calls int
}

func (c *capturedExec) fn(argv0 string, argv []string, envv []string) error {
	c.argv0 = argv0
	c.argv = append([]string(nil), argv...)
	c.envv = append([]string(nil), envv...)
	c.calls++
	return nil
}

// makeBindings writes a bindings JSON file into storeRoot and returns the path.
func makeBindings(t *testing.T, storeRoot string, bindings []HerdrSpaceBinding) {
	t.Helper()
	data, err := json.Marshal(bindings)
	if err != nil {
		t.Fatalf("marshal bindings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeRoot, "herdr-space-bindings.json"), data, 0o644); err != nil {
		t.Fatalf("write bindings: %v", err)
	}
}

// testBinding is a canonical binding used across tests.
var testBinding = HerdrSpaceBinding{
	SpaceLabel:       "nexus3:ac3/testbox",
	HerdrWorkspaceID: "wXX",
	SandboxHandle:    "ac3/testbox",
	SandboxID:        "sb-TESTID",
}

// runCore is a convenience wrapper that calls herdrDefaultShellCore with a
// fake nexus3 binary path.
func runCore(
	ctx context.Context,
	getenv func(string) string,
	storeRoot string,
	svc sandboxGetter,
	execFn herdrExecFn,
) error {
	return herdrDefaultShellCore(ctx, getenv, storeRoot, svc, "/fake/nexus3", execFn)
}

// TestHerdrDefaultShell_BoundWorkspace verifies that a workspace bound to a
// sandbox causes the guest shell to be exec'd, at the right cwd, for the
// correct sandbox handle. The assertion checks the exact handle identity —
// not merely "something was exec'd".
//
// Mutation proof: change binding.SandboxHandle to a different string in
// herdrDefaultShellCore → test catches the wrong handle in argv.
func TestHerdrDefaultShell_BoundWorkspace(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	// Sandbox is running with a live mount at /work — that becomes the cwd.
	svc := &fakeDefaultShellGetter{
		sb: domain.Sandbox{
			State:      domain.Running,
			LiveMounts: []domain.LiveMount{{GuestPath: "/work"}},
		},
	}

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return "wXX"
		case "SHELL":
			return "/bin/zsh"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, svc, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.calls != 1 {
		t.Fatalf("exec called %d times, want 1", cap.calls)
	}

	// argv0 must be the nexus3 binary — we are re-exec'ing nexus3.
	if cap.argv0 != "/fake/nexus3" {
		t.Errorf("argv0 = %q, want /fake/nexus3 (nexus3 binary for re-exec)", cap.argv0)
	}
	// argv[1] must be "exec" (the nexus3 subcommand).
	if len(cap.argv) < 2 || cap.argv[1] != "exec" {
		t.Errorf("argv[1] = %q, want \"exec\"", safeIdx(cap.argv, 1))
	}
	// "--pty" must be present.
	if !argvContains(cap.argv, "--pty") {
		t.Errorf("argv %v missing --pty flag", cap.argv)
	}
	// "--cwd" followed by "/work" must be present — cwd resolved from live mount.
	cwdIdx := argvIndexOf(cap.argv, "--cwd")
	if cwdIdx < 0 || cwdIdx+1 >= len(cap.argv) || cap.argv[cwdIdx+1] != "/work" {
		t.Errorf("argv %v: expected --cwd /work", cap.argv)
	}
	// The exact sandbox handle must appear in argv — identity, not just
	// "some string after --cwd".
	if !argvContains(cap.argv, testBinding.SandboxHandle) {
		t.Errorf("argv %v missing sandbox handle %q", cap.argv, testBinding.SandboxHandle)
	}
	// The host shell (/bin/zsh) must NOT appear — we exec the guest shell.
	if argvContains(cap.argv, "/bin/zsh") {
		t.Errorf("argv %v unexpectedly contains host shell", cap.argv)
	}
}

// TestHerdrDefaultShell_NoWorkspaceID verifies that an unset HERDR_WORKSPACE_ID
// causes the host shell to be exec'd.
//
// Mutation proof: remove the wsID == "" guard in herdrDefaultShellCore →
// the code reaches herdrDefaultShellLookup with wsID="". A binding with
// HerdrWorkspaceID="" is included specifically to make the lookup succeed in
// that case, causing the mutant to exec the guest shell and fail the assertion.
func TestHerdrDefaultShell_NoWorkspaceID(t *testing.T) {
	root := t.TempDir()
	// The empty-ID binding ensures the wsID=="" guard removal is detectable:
	// without the guard, lookup finds this binding and exec's the guest shell.
	makeBindings(t, root, []HerdrSpaceBinding{
		testBinding,
		{HerdrWorkspaceID: "", SandboxHandle: "dummy/empty-id"},
	})

	cap := &capturedExec{}
	getenv := func(k string) string {
		if k == "SHELL" {
			return "/bin/bash"
		}
		return "" // HERDR_WORKSPACE_ID unset
	}

	if err := runCore(context.Background(), getenv, root, nil, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// TestHerdrDefaultShell_WorkspaceNotInBindings verifies that a workspace ID
// present in the environment but absent from the bindings file causes the host
// shell to be exec'd.
//
// Mutation proof: remove the !found guard → test fails because the zero-value
// binding (empty SandboxHandle) is used, and exec is called with the nexus3
// binary and an empty handle.
func TestHerdrDefaultShell_WorkspaceNotInBindings(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return "wUNKNOWN" // not in bindings
		case "SHELL":
			return "/usr/bin/fish"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, nil, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHostShell(t, cap, "/usr/bin/fish")
}

// TestHerdrDefaultShell_BindingsFileMissing verifies that a missing bindings
// file causes the host shell to be exec'd without error.
//
// Mutation proof: return an error from herdrDefaultShellLookup on ErrNotExist
// instead of (false, nil) → test still passes (both code paths reach
// execHostShell), so this tests the "no error to operator" constraint: err
// must be nil from runCore.
func TestHerdrDefaultShell_BindingsFileMissing(t *testing.T) {
	root := t.TempDir()
	// No bindings file written.

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return "wXX"
		case "SHELL":
			return "/bin/sh"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, nil, cap.fn); err != nil {
		t.Fatalf("missing bindings file must not return error to operator, got: %v", err)
	}
	assertHostShell(t, cap, "/bin/sh")
}

// TestHerdrDefaultShell_BindingsFileMalformed verifies that a syntactically
// invalid JSON bindings file causes the host shell to be exec'd without
// surfacing an error to the operator.
//
// Mutation proof: remove the json.Unmarshal error guard in herdrDefaultShellLookup
// → the unmarshal returns a nil/zero slice, the loop finds no binding, and
// execHostShell is still reached — so this test ALSO verifies no panic.
// Strengthen: check that execFn was called exactly once with the host shell.
func TestHerdrDefaultShell_BindingsFileMalformed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "herdr-space-bindings.json"), []byte("not json {{"), 0o644); err != nil {
		t.Fatalf("write malformed bindings: %v", err)
	}

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return "wXX"
		case "SHELL":
			return "/bin/zsh"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, nil, cap.fn); err != nil {
		t.Fatalf("malformed bindings must not return error to operator, got: %v", err)
	}
	// exec must have been called exactly once with the host shell.
	assertHostShell(t, cap, "/bin/zsh")
}

// TestHerdrDefaultShell_PlainNexus3WorkspaceNoBinding tests the w8-shaped
// case: a workspace whose label would be plain "nexus3" (no colon) has no
// binding in the file. The workspace ID is simply not present, so the host
// shell is chosen. This confirms nothing in the lookup accidentally matches on
// partial label fields.
//
// Note: we match by HerdrWorkspaceID, not by SpaceLabel, so label format is
// irrelevant here — the real guard is the absence of a binding entry for that
// workspace ID.
func TestHerdrDefaultShell_PlainNexus3WorkspaceNoBinding(t *testing.T) {
	root := t.TempDir()
	// Bindings exist for a nexus3 space, but NOT for the operator's own workspace.
	makeBindings(t, root, []HerdrSpaceBinding{
		{
			SpaceLabel:       "nexus3:ac3/testbox",
			HerdrWorkspaceID: "wXX",
			SandboxHandle:    "ac3/testbox",
			SandboxID:        "sb-TESTID",
		},
	})

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return "w8" // operator's own workspace — no nexus3 binding
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, nil, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// TestHerdrDefaultShell_EscapeHatch verifies that NEXUS3_HOST_SHELL=1 forces
// the host shell even when a valid binding exists.
//
// Mutation proof: remove the NEXUS3_HOST_SHELL guard in herdrDefaultShellCore
// → test fails because exec is called with the nexus3 binary (guest exec path)
// instead of the host shell.
func TestHerdrDefaultShell_EscapeHatch(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	svc := &fakeDefaultShellGetter{
		sb: domain.Sandbox{
			State:      domain.Running,
			LiveMounts: []domain.LiveMount{{GuestPath: "/work"}},
		},
	}

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "NEXUS3_HOST_SHELL":
			return "1"
		case "HERDR_WORKSPACE_ID":
			return "wXX"
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, svc, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// TestHerdrDefaultShellLookup_ReturnsCorrectBinding verifies that
// herdrDefaultShellLookup returns the binding matching the requested workspace
// ID and not a different one.
//
// Mutation proof: change the equality check in herdrDefaultShellLookup from
// b.HerdrWorkspaceID == wsID to b.HerdrWorkspaceID != wsID → test catches
// the wrong binding returned (wrong SandboxHandle).
func TestHerdrDefaultShellLookup_ReturnsCorrectBinding(t *testing.T) {
	root := t.TempDir()
	b1 := HerdrSpaceBinding{HerdrWorkspaceID: "wAA", SandboxHandle: "proj/alpha"}
	b2 := HerdrSpaceBinding{HerdrWorkspaceID: "wBB", SandboxHandle: "proj/beta"}
	makeBindings(t, root, []HerdrSpaceBinding{b1, b2})

	got, found, err := herdrDefaultShellLookup(root, "wBB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected binding found=true for wBB")
	}
	// Identity assertion: the exact handle, not just any non-zero binding.
	if got.SandboxHandle != "proj/beta" {
		t.Errorf("SandboxHandle = %q, want %q", got.SandboxHandle, "proj/beta")
	}
}

// TestHerdrDefaultShell_NilServiceFallsBackToCwd verifies that when svc is
// nil (daemon unreachable), cwd defaults to /root and the guest exec still
// proceeds.
//
// Mutation proof: remove the svc != nil guard → nil pointer dereference causes
// a panic, producing an unmistakable RED.
func TestHerdrDefaultShell_NilServiceFallsBackToCwd(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	cap := &capturedExec{}
	getenv := func(k string) string {
		if k == "HERDR_WORKSPACE_ID" {
			return "wXX"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, nil /* svc=nil */, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should exec the guest shell (not host shell) using /root as cwd.
	if cap.argv0 != "/fake/nexus3" {
		t.Errorf("argv0 = %q, want /fake/nexus3 (guest exec)", cap.argv0)
	}
	cwdIdx := argvIndexOf(cap.argv, "--cwd")
	if cwdIdx < 0 || cwdIdx+1 >= len(cap.argv) || cap.argv[cwdIdx+1] != "/root" {
		t.Errorf("argv %v: expected --cwd /root as fallback cwd", cap.argv)
	}
}

// assertHostShell checks that execFn was called once with the host shell.
// The shell identity is checked against wantShell — catching mutations that
// pass the wrong shell or forget the exec entirely.
func assertHostShell(t *testing.T, cap *capturedExec, wantShell string) {
	t.Helper()
	if cap.calls != 1 {
		t.Fatalf("exec called %d times, want 1", cap.calls)
	}
	if cap.argv0 != wantShell {
		t.Errorf("argv0 = %q, want %q (host shell)", cap.argv0, wantShell)
	}
	// argv[0] must also match — shell convention (process name in argv[0]).
	if len(cap.argv) == 0 || cap.argv[0] != wantShell {
		t.Errorf("argv[0] = %q, want %q", safeIdx(cap.argv, 0), wantShell)
	}
	// Must not contain "exec" subcommand — that would indicate the guest path.
	if len(cap.argv) > 1 && cap.argv[1] == "exec" {
		t.Errorf("host shell exec must not use nexus3 exec subcommand; argv = %v", cap.argv)
	}
}

// safeIdx returns argv[i] or "" if i is out of range.
func safeIdx(argv []string, i int) string {
	if i < len(argv) {
		return argv[i]
	}
	return ""
}

// argvContains reports whether s appears in argv.
func argvContains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

// argvIndexOf returns the index of s in argv, or -1 if absent.
func argvIndexOf(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}

// TestHerdrDefaultShell_GuestNotDialable verifies that when the sandbox record
// says Running but the vsock dial fails (e.g. substrate/driver not resolved
// because PATH lacks the hypervisor binary), the host shell is exec'd rather
// than attempting "nexus3 exec --pty" after the point of no return.
//
// This covers the CRITICAL gap: reproduced live as:
//
//	env -i HOME=... PATH=/usr/bin:/bin HERDR_WORKSPACE_ID=w34 ~/.local/bin/nexus3-guest-shell
//	→ error: exec: service: agent: driver "none" does not support guest dialing:
//	        service: no substrate configured
//
// (dead pane; "State == Running" was true but the substrate was not reachable)
//
// Mutation proof: remove the `if d, ok := svc.(sandboxDialer); ok` block (or
// its `dialErr != nil` return) in herdrDefaultShellCore → execFn is called
// with "/fake/nexus3" as argv0 (the guest exec path). assertHostShell checks
// argv0 == "/bin/bash" but gets "/fake/nexus3" → RED.
func TestHerdrDefaultShell_GuestNotDialable(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	svc := &fakeDialableGetter{
		fakeDefaultShellGetter: fakeDefaultShellGetter{
			sb: domain.Sandbox{
				State:      domain.Running,
				LiveMounts: []domain.LiveMount{{GuestPath: "/work"}},
			},
		},
		dialErr: errors.New(`driver "none" does not support guest dialing: service: no substrate configured`),
	}

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return testBinding.HerdrWorkspaceID
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, svc, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Dial failed → must exec host shell, not nexus3 exec.
	assertHostShell(t, cap, "/bin/bash")
	if !svc.dialed {
		t.Error("DialGuest was never called — dialability check not exercised")
	}
	// Verify IDENTITY: the dial targeted the correct sandbox on the correct port.
	// Mutation proof: changing the ref or port in herdrDefaultShellCore to wrong
	// values still dials, but these assertions fail RED.
	if svc.dialedRef != testBinding.SandboxHandle {
		t.Errorf("DialGuest ref = %q, want %q (sandbox handle)", svc.dialedRef, testBinding.SandboxHandle)
	}
	if svc.dialedPort != driver.AgentControlPort {
		t.Errorf("DialGuest port = %d, want %d (AgentControlPort)", svc.dialedPort, driver.AgentControlPort)
	}
}

// TestHerdrDefaultShell_GuestDialable verifies the happy path with a
// sandboxDialer: dial succeeds, guest exec is performed.
//
// Without this test, a mutation that removes the dial check entirely would
// still appear RED via TestHerdrDefaultShell_GuestNotDialable (because the
// fake's dial error path is also gone). This test verifies the successful
// dial path also reaches exec rather than host shell.
func TestHerdrDefaultShell_GuestDialable(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	svc := &fakeDialableGetter{
		fakeDefaultShellGetter: fakeDefaultShellGetter{
			sb: domain.Sandbox{
				State:      domain.Running,
				LiveMounts: []domain.LiveMount{{GuestPath: "/work"}},
			},
		},
		// dialErr nil → dial succeeds
	}

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return testBinding.HerdrWorkspaceID
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, svc, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Dial succeeded → must exec the guest shell (nexus3 exec), not host shell.
	if cap.argv0 != "/fake/nexus3" {
		t.Errorf("argv0 = %q, want /fake/nexus3 (guest exec after successful dial)", cap.argv0)
	}
	if !svc.dialed {
		t.Error("DialGuest was never called — dialability check not exercised")
	}
	// Verify IDENTITY: the dial targeted the correct sandbox on the correct port.
	if svc.dialedRef != testBinding.SandboxHandle {
		t.Errorf("DialGuest ref = %q, want %q (sandbox handle)", svc.dialedRef, testBinding.SandboxHandle)
	}
	if svc.dialedPort != driver.AgentControlPort {
		t.Errorf("DialGuest port = %d, want %d (AgentControlPort)", svc.dialedPort, driver.AgentControlPort)
	}
}

// TestHerdrInstallDefaultShell_ProbeStructure verifies that herdrInstallProbeCmd
// constructs the probe command with the correct invariants, without running it.
//
// The probe is skipped in all unit tests (herdrSkipInstallProbeForTest=true),
// so a broken probe (wrong Args, misspelled env key) would install a shell
// that silently fails. This test covers those invariants:
//   - Args[0] == "nexus3-guest-shell" triggers the argv[0] dispatch in main.go
//   - NEXUS3_HOST_SHELL=1 causes herdrDefaultShellCore to exec $SHELL immediately
//   - SHELL=/bin/true gives a clean exit 0 to confirm the dispatch works
//
// Mutation proof: change Args[0] from "nexus3-guest-shell" to "nexus3" in
// herdrInstallProbeCmd → test fails because Args[0] != "nexus3-guest-shell" → RED.
func TestHerdrInstallDefaultShell_ProbeStructure(t *testing.T) {
	cmd := herdrInstallProbeCmd("/fake/install/path")

	// Mutation proof: change Path from installPath to "/bin/true" — a probe
	// that can never fail — and this assertion turns RED.
	if cmd.Path != "/fake/install/path" {
		t.Errorf("probe Path = %q, want %q (the installed binary, not a surrogate)",
			cmd.Path, "/fake/install/path")
	}

	if len(cmd.Args) == 0 || cmd.Args[0] != "nexus3-guest-shell" {
		t.Errorf("probe Args[0] = %q, want \"nexus3-guest-shell\" (argv[0] dispatch trigger)",
			safeIdx(cmd.Args, 0))
	}

	var hasHostShell, hasShellBin bool
	for _, e := range cmd.Env {
		if e == "NEXUS3_HOST_SHELL=1" {
			hasHostShell = true
		}
		if e == "SHELL=/bin/true" {
			hasShellBin = true
		}
	}
	if !hasHostShell {
		t.Error("probe Env missing NEXUS3_HOST_SHELL=1 — escape hatch will not fire; probe hangs on stdin")
	}
	if !hasShellBin {
		t.Error("probe Env missing SHELL=/bin/true — probe will not get a clean exit 0")
	}
}

// TestHerdrDefaultShell_ShellFallbackToSlashSh verifies that when $SHELL is
// unset, /bin/sh is used as the host shell.
func TestHerdrDefaultShell_ShellFallbackToSlashSh(t *testing.T) {
	root := t.TempDir()
	// No bindings — this workspace ID is unknown.
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	cap := &capturedExec{}
	getenv := func(k string) string {
		if k == "HERDR_WORKSPACE_ID" {
			return "wMISSING"
		}
		return "" // SHELL unset
	}

	if err := runCore(context.Background(), getenv, root, nil, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// $SHELL unset → /bin/sh
	if cap.argv0 != "/bin/sh" {
		t.Errorf("argv0 = %q, want /bin/sh (fallback when $SHELL unset)", cap.argv0)
	}
}

// TestHerdrInstallDefaultShell verifies that install-default-shell creates a
// hard link (or copy) of the nexus3 binary at ~/.local/bin/nexus3-guest-shell,
// writes the sidecar with the nexus3 binary path, and prints the config snippet.
//
// The installed file is a binary, not a shell script — no PATH lookup at
// runtime (CRITICAL 1), and survives rebuilds via hard-link inode decoupling
// (CRITICAL 2). The probe is skipped in tests (herdrSkipInstallProbeForTest).
func TestHerdrInstallDefaultShell(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	out := &Output{w: &strings.Builder{}}
	if err := runHerdrInstallDefaultShell(context.Background(), nil, out); err != nil {
		t.Fatalf("install-default-shell: %v", err)
	}

	installPath := filepath.Join(tmpHome, ".local", "bin", "nexus3-guest-shell")
	sidecarPath := installPath + herdrSidecarSuffix // MAJOR 8: single constant, no divergence

	// Installed binary must exist and be executable.
	info, err := os.Stat(installPath)
	if err != nil {
		t.Fatalf("installed binary not created: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("installed binary not executable: mode %v", info.Mode())
	}

	// Installed file must NOT be a shell script (not a plain-text wrapper).
	// Mutation proof: if runHerdrInstallDefaultShell writes a shell script
	// (e.g. "#!/bin/sh\nexec nexus3 herdr default-shell\n"), the file starts
	// with "#!" and this check fails RED — catching the broken-wrapper regression.
	{
		f, openErr := os.Open(installPath)
		if openErr != nil {
			t.Fatalf("cannot open installed binary for inspection: %v", openErr)
		}
		header := make([]byte, 2)
		n, _ := f.Read(header)
		f.Close()
		if n >= 2 && header[0] == '#' && header[1] == '!' {
			t.Errorf("installed file starts with #! — it is a shell script, want a real binary (hard link or copy)")
		}
	}

	// Hard-link or copy: verify inode identity when possible.
	if info.Sys() != nil {
		self, exErr := os.Executable()
		if exErr == nil {
			self, _ = filepath.EvalSymlinks(self)
			selfStat, selfStatErr := os.Stat(self)
			if selfStatErr == nil && !os.SameFile(info, selfStat) {
				// Cross-device copy path: acceptable — verify non-zero size.
				if info.Size() == 0 {
					t.Error("installed binary is zero-length (neither hard-link nor copy succeeded)")
				}
			}
		}
	}

	// Sidecar must exist and line 1 must equal the real nexus3 binary path
	// (the installer writes os.Executable() as line 1). Non-emptiness alone is
	// not sufficient: a wrong-but-executable path (e.g. "/bin/echo") passes the
	// empty check, but syscall.Exec replaces the process with echo and leaves a
	// dead pane — the only unsafe corruption that bypasses fail-open.
	// Line 2 carries the kernel path and may be empty if unresolved at install.
	sidecarData, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}
	lines := strings.SplitN(strings.TrimRight(string(sidecarData), "\n"), "\n", 2)
	expectedBin, _ := os.Executable()
	if len(lines) == 0 || lines[0] != expectedBin {
		t.Errorf("sidecar line 1 = %q, want nexus3 binary path %q", func() string {
			if len(lines) == 0 {
				return ""
			}
			return lines[0]
		}(), expectedBin)
	}

	// Output must include the config snippet pointing at the install path.
	snippet := out.w.(*strings.Builder).String()
	if !strings.Contains(snippet, "default_shell") {
		t.Errorf("output %q missing default_shell key", snippet)
	}
	if !strings.Contains(snippet, installPath) {
		t.Errorf("output %q missing install path %q", snippet, installPath)
	}
}

// TestHerdrDefaultShell_EmptyNexus3Bin verifies that an empty nexus3Bin causes
// the host shell to be exec'd rather than attempting exec with an empty path.
//
// This is the unit-level equivalent of CRITICAL 1: when the delivery mechanism
// (sidecar file) is missing after install, herdrReadSidecar() returns ("", "")
// and the core must fall through to the host shell, not exec("", ...).
//
// Mutation proof: remove the nexus3Bin == "" guard in herdrDefaultShellCore →
// execFn is called with argv0="" and argv[0]="" instead of argv0="/bin/zsh".
// The assertion checks the exact argv0, so "" ≠ "/bin/zsh" → RED.
func TestHerdrDefaultShell_EmptyNexus3Bin(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return testBinding.HerdrWorkspaceID
		case "SHELL":
			return "/bin/zsh"
		}
		return ""
	}

	// nexus3Bin="" with svc=nil: binding found, but empty binary path → host shell.
	if err := herdrDefaultShellCore(context.Background(), getenv, root, nil, "" /* nexus3Bin */, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must exec the host shell, NOT an empty argv0.
	assertHostShell(t, cap, "/bin/zsh")
}

// TestHerdrDefaultShell_SandboxNotRunning verifies that a bound workspace
// whose sandbox is not in Running state falls back to the host shell rather
// than exec'ing "nexus3 exec --pty" after the point of no return.
//
// Mutation proof: remove the sb.State != domain.Running check in
// herdrDefaultShellCore → execFn is called with "/fake/nexus3" as argv0
// (the guest exec path). The assertion checks for the host shell argv0, so
// "/fake/nexus3" ≠ "/bin/bash" → RED.
func TestHerdrDefaultShell_SandboxNotRunning(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	for _, state := range []domain.State{domain.Paused, domain.Stopped, domain.Error} {
		t.Run(state.String(), func(t *testing.T) {
			svc := &fakeDefaultShellGetter{
				sb: domain.Sandbox{State: state},
			}
			cap := &capturedExec{}
			getenv := func(k string) string {
				switch k {
				case "HERDR_WORKSPACE_ID":
					return testBinding.HerdrWorkspaceID
				case "SHELL":
					return "/bin/bash"
				}
				return ""
			}
			if err := runCore(context.Background(), getenv, root, svc, cap.fn); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertHostShell(t, cap, "/bin/bash")
		})
	}
}

// TestHerdrDefaultShell_SandboxGetError verifies that an error from svc.Get
// (e.g. daemon unreachable after the service was initially available) falls
// back to the host shell rather than proceeding to exec.
//
// Mutation proof: change `sbErr != nil || sb.State != domain.Running` to
// `sbErr == nil && sb.State != domain.Running` in herdrDefaultShellCore.
// When sbErr != nil (error case): `sbErr == nil` is false → AND short-circuits
// → condition is false → falls through to exec with "/fake/nexus3" as argv0.
// assertHostShell checks argv0 == "/bin/bash" but gets "/fake/nexus3" → RED.
// (Verified: FAIL — "argv0 = "/fake/nexus3", want "/bin/bash" (host shell)")
func TestHerdrDefaultShell_SandboxGetError(t *testing.T) {
	root := t.TempDir()
	makeBindings(t, root, []HerdrSpaceBinding{testBinding})

	svc := &fakeDefaultShellGetter{err: errors.New("daemon unreachable")}
	cap := &capturedExec{}
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return testBinding.HerdrWorkspaceID
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	if err := runCore(context.Background(), getenv, root, svc, cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// TestRunHerdrGuestShell_PanicRecovery verifies that a panic anywhere in the
// RunHerdrGuestShell resolution path is caught by the top-level defer recover()
// and the operator gets their host shell (CRITICAL 3).
//
// Mutation proof: change the recover handler from "exec host shell + exit 0" to
// "panic(r)" (re-panic) in RunHerdrGuestShell. The re-panic propagates to the
// test process, causing a FAIL with the runtime panic output. The "remove defer
// recover() block" mutation is invalid — it produces a syntax error from the
// mutation script, not a compilable mutant.
// (Verified: FAIL — test process killed with "panic: simulated panic for CRITICAL 3")
func TestRunHerdrGuestShell_PanicRecovery(t *testing.T) {
	origExec := herdrGuestShellExecFn
	origExit := herdrGuestShellExitFn
	t.Cleanup(func() {
		herdrGuestShellExecFn = origExec
		herdrGuestShellExitFn = origExit
	})

	calls := 0
	cap := &capturedExec{}
	herdrGuestShellExecFn = func(argv0 string, argv []string, envv []string) error {
		calls++
		if calls == 1 {
			// First exec call (host shell from NEXUS3_HOST_SHELL=1 path):
			// panic to simulate an unexpected bug mid-resolution.
			panic("simulated panic for CRITICAL 3")
		}
		// Second call: inside the recover block — capture it.
		return cap.fn(argv0, argv, envv)
	}

	exitedWith := -1
	herdrGuestShellExitFn = func(code int) { exitedWith = code }

	// NEXUS3_HOST_SHELL=1 routes herdrDefaultShellCore to execHostShell early,
	// ensuring the first execFn call happens with minimal setup so the panic
	// is predictable.
	t.Setenv("NEXUS3_HOST_SHELL", "1")
	t.Setenv("SHELL", "/bin/bash")

	RunHerdrGuestShell() // must not crash the test process

	if exitedWith != 0 {
		t.Errorf("expected clean exit 0 after panic recovery, got %d", exitedWith)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// TestHerdrApplyKernelPath locks the CRITICAL 1 fix.
//
// The hard link installs into ~/.local/bin, which has no images/ sibling, so
// resolveKernelPath falls through to <cwd>/images/… — and herdr opens panes
// from /home/<user>, not the repo root. Without the stamped kernel path,
// substrate selection returns the noop driver, DialGuest fails, and every
// sandbox pane silently gets a host shell. That is how the feature shipped
// "live-proven" and inert: every proof was run from the repo root.
//
// Deleting the function body of herdrApplyKernelPath previously left the whole
// suite green — this test bites only on that mutation (function body deleted).
// The call site in RunHerdrGuestShell was separately uncovered until
// TestRunHerdrGuestShell_KernelPathPublished was added; that test bites on the
// `_ = kernelPath` (call-site suppressed) mutation instead.
func TestHerdrApplyKernelPath(t *testing.T) {
	const stamped = "/stamped/vmlinux-x86_64"

	for _, tc := range []struct {
		name       string
		kernelPath string
		existing   string
		wantSet    bool
		wantValue  string
	}{
		{name: "stamps when unset", kernelPath: stamped, existing: "", wantSet: true, wantValue: stamped},
		{name: "operator override wins", kernelPath: stamped, existing: "/operator/kernel", wantSet: false},
		{name: "empty stamp is a no-op", kernelPath: "", existing: "", wantSet: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotKey, gotValue string
			var calls int
			setenv := func(k, v string) error {
				calls++
				gotKey, gotValue = k, v
				return nil
			}
			var askedKey string
			getenv := func(key string) string {
				askedKey = key
				if key == "NEXUS3_KERNEL_PATH" {
					return tc.existing
				}
				return ""
			}

			herdrApplyKernelPath(tc.kernelPath, getenv, setenv)

			// Pin the exact env key so a typo in the production lookup (e.g.
			// "NEXUS3_KERNEL_PATH_TYPO") turns RED: getenv would return "" for
			// the wrong key and setenv would be called even when tc.existing is
			// set, failing the wantSet:false "operator override wins" case.
			if tc.kernelPath != "" && askedKey != "NEXUS3_KERNEL_PATH" {
				t.Errorf("getenv called with key %q, want %q", askedKey, "NEXUS3_KERNEL_PATH")
			}

			if tc.wantSet {
				if calls != 1 {
					t.Fatalf("setenv calls = %d, want 1 — the stamped kernel path was not published", calls)
				}
				if gotKey != "NEXUS3_KERNEL_PATH" {
					t.Errorf("setenv key = %q, want %q", gotKey, "NEXUS3_KERNEL_PATH")
				}
				if gotValue != tc.wantValue {
					t.Errorf("setenv value = %q, want %q", gotValue, tc.wantValue)
				}
				return
			}
			if calls != 0 {
				t.Fatalf("setenv called %d times with %s=%q; want no call", calls, gotKey, gotValue)
			}
		})
	}
}

// TestRunHerdrGuestShell_KernelPathPublished covers the call site of
// herdrApplyKernelPath inside RunHerdrGuestShell (Finding 2).
//
// TestHerdrApplyKernelPath proves the function body works in isolation, but the
// call `herdrApplyKernelPath(kernelPath, os.Getenv, os.Setenv)` at the
// RunHerdrGuestShell level was untested: replacing it with `_ = kernelPath`
// left the entire suite green. This test closes that gap.
//
// Mutation proof: replace the call with `_ = kernelPath` in RunHerdrGuestShell.
// The sidecar contains "/stamped/kernel" as line 2, but NEXUS3_KERNEL_PATH is
// never set, so os.Getenv("NEXUS3_KERNEL_PATH") returns "" after the call →
// t.Errorf fires → RED.
func TestRunHerdrGuestShell_KernelPathPublished(t *testing.T) {
	origExec := herdrGuestShellExecFn
	origExit := herdrGuestShellExitFn
	t.Cleanup(func() {
		herdrGuestShellExecFn = origExec
		herdrGuestShellExitFn = origExit
	})

	// Capture exec calls so RunHerdrGuestShell does not actually exec-replace.
	herdrGuestShellExecFn = func(argv0 string, argv []string, envv []string) error {
		return nil // captured; process continues
	}
	herdrGuestShellExitFn = func(int) {} // suppress os.Exit

	// Write a sidecar next to the test binary so herdrReadSidecar finds it.
	// Line 1 = os.Executable() (must stat-check clean).
	// Line 2 = stamped kernel path under test.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	const stampedKernel = "/stamped/vmlinux-kernel-path-published"
	sidecarPath := self + herdrSidecarSuffix
	if err := os.WriteFile(sidecarPath, []byte(self+"\n"+stampedKernel+"\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	t.Cleanup(func() { os.Remove(sidecarPath) })

	// Clear NEXUS3_KERNEL_PATH so herdrApplyKernelPath has something to set.
	t.Setenv("NEXUS3_KERNEL_PATH", "")
	// Route herdrDefaultShellCore to execHostShell immediately — keeps the rest
	// of the resolution simple and predictable.
	t.Setenv("NEXUS3_HOST_SHELL", "1")
	t.Setenv("SHELL", "/bin/bash")

	RunHerdrGuestShell()

	got := os.Getenv("NEXUS3_KERNEL_PATH")
	if got != stampedKernel {
		t.Errorf("NEXUS3_KERNEL_PATH = %q after RunHerdrGuestShell, want %q — herdrApplyKernelPath call site may be suppressed", got, stampedKernel)
	}
}

// ── auto-create in herdrDefaultShellCore ─────────────────────────────────────

func TestHerdrDefaultShell_UnboundWorktree_AutoCreateSucceeds(t *testing.T) {
	// An unbound workspace that succeeds auto-create must run the guest shell
	// via herdrWtChildRunnerFn (supervised mode), not the host shell.
	// wt/ handles take the supervised path so execFn is not called.
	//
	// MUTATION PROOF: remove the herdrDefaultShellAutoCreateFn call in
	// herdrDefaultShellCore → auto-create never fires → workspace stays unbound
	// → execHostShell is returned → herdrWtChildRunnerFn never called. RED.
	//
	// MUTATION PROOF (wt/ branch): remove the isHerdrWorktreeHandle guard in
	// herdrDefaultShellCore → wt/ handle goes to execFn instead of supervised
	// path → herdrWtChildRunnerFn never called → test RED.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wsID := "wWT1"

	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:wt/feat",
		HerdrWorkspaceID: wsID,
		SandboxHandle:    "wt/feat",
		SandboxID:        "sb-wt1",
	}

	// Stub the predicate to return true (simulates: linked worktree + binding exists).
	// MUTATION PROOF (predicate gate): set predFn to return false → auto-create
	// is never reached → execHostShell → herdrWtChildRunnerFn never called. RED.
	oldPred := herdrAutoCreatePredicateFn
	herdrAutoCreatePredicateFn = func(_ []HerdrSpaceBinding) bool { return true }
	t.Cleanup(func() { herdrAutoCreatePredicateFn = oldPred })

	// Stub: auto-create succeeds and returns the binding.
	old := herdrDefaultShellAutoCreateFn
	herdrDefaultShellAutoCreateFn = func(_ context.Context, _, gotWsID, _ string, _ io.Writer) (HerdrSpaceBinding, bool) {
		if gotWsID != wsID {
			t.Errorf("auto-create called with wsID=%q; want %q", gotWsID, wsID)
		}
		return binding, true
	}
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = old })

	// Stub herdrWtChildRunnerFn to record calls and return immediately.
	oldChild := herdrWtChildRunnerFn
	var childArgv []string
	herdrWtChildRunnerFn = func(_ context.Context, _ string, argv []string) error {
		childArgv = argv
		return nil
	}
	t.Cleanup(func() { herdrWtChildRunnerFn = oldChild })

	// Stub herdrWtPaneListerFn to return 1 remaining pane (so remover not called).
	oldPaner := herdrWtPaneListerFn
	herdrWtPaneListerFn = func(_ context.Context, _, _ string) (int, error) { return 1, nil }
	t.Cleanup(func() { herdrWtPaneListerFn = oldPaner })

	// Stub herdrWtSandboxRemoverFn: must not be called in this test.
	oldRemov := herdrWtSandboxRemoverFn
	herdrWtSandboxRemoverFn = func(_ context.Context, h string) error {
		t.Errorf("herdrWtSandboxRemoverFn called unexpectedly for handle %q", h)
		return nil
	}
	t.Cleanup(func() { herdrWtSandboxRemoverFn = oldRemov })

	// Use a svc that reports Running + dialable.
	svc := &fakeDialableGetter{
		fakeDefaultShellGetter: fakeDefaultShellGetter{
			sb: domain.Sandbox{State: domain.Running},
		},
	}

	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return wsID
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	cap := &capturedExec{}
	// Pass an EMPTY store root so the bindings file lookup returns !found.
	emptyRoot := t.TempDir()
	if err := herdrDefaultShellCore(context.Background(), getenv, emptyRoot, svc, "/fake/nexus3", cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must have called herdrWtChildRunnerFn with the guest-exec argv (not host shell).
	if len(childArgv) == 0 {
		t.Fatal("herdrWtChildRunnerFn was not called; wt/ binding must use supervised path")
	}
	// The argv must target nexus3 exec, not the host shell.
	if len(childArgv) > 0 && (childArgv[0] == "/bin/bash" || childArgv[0] == "/bin/sh") {
		t.Errorf("herdrWtChildRunnerFn called with host shell %q; want nexus3 exec argv", childArgv[0])
	}
	// execFn (cap) must NOT have been called — wt/ takes the supervised path.
	if cap.argv0 != "" {
		t.Errorf("execFn called for wt/ binding (argv0=%q); wt/ must use supervised path, not exec-replace", cap.argv0)
	}
}

func TestHerdrDefaultShell_UnboundNonWorktree_NoSpawn_HostShell(t *testing.T) {
	// An unbound NON-worktree workspace must reach the host shell with ZERO
	// output and ZERO calls to herdrDefaultShellAutoCreateFn. This is the path
	// for w6/w8/w2R — plain operator workspaces that are NOT linked worktrees.
	//
	// The predicate herdrAutoCreatePredicateFn returns false for a non-worktree
	// workspace, so the auto-create path is never entered. Any output before the
	// predicate passes is itself a failure (e.g. a false "linked worktree
	// detected" message).
	//
	// MUTATION PROOF (predicate gate removed): bypass the predicate check in
	// herdrDefaultShellCore → auto-create stub is called → autoCreateCalled
	// becomes true → assertion fires RED.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wsID := "w8"

	autoCreateCalled := false
	old := herdrDefaultShellAutoCreateFn
	herdrDefaultShellAutoCreateFn = func(_ context.Context, _, _ string, _ string, _ io.Writer) (HerdrSpaceBinding, bool) {
		autoCreateCalled = true
		return HerdrSpaceBinding{}, false
	}
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = old })

	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return wsID
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	cap := &capturedExec{}
	// emptyRoot has no bindings file → (c) is false → predicate returns false
	// → auto-create path is NOT entered.
	emptyRoot := t.TempDir()
	if err := herdrDefaultShellCore(context.Background(), getenv, emptyRoot, nil, "/fake/nexus3", cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The auto-create fn must NOT have been called.
	if autoCreateCalled {
		t.Error("auto-create stub was called for a non-worktree workspace — predicate gate is not wired")
	}
	assertHostShell(t, cap, "/bin/bash")
}

func TestHerdrDefaultShell_AutoCreateFails_HostShell(t *testing.T) {
	// When the predicate passes (cwd is inside a linked worktree with a
	// matching RepoRoot binding) but auto-create returns false, execution
	// must fall through to execHostShell rather than continuing with a
	// zero-value binding.
	//
	// MUTATION PROOF (fail-open guard at :267-269 removed): with svc==nil,
	// execution falls through to the guest exec with an empty SandboxHandle
	// → this test goes RED.
	dir := t.TempDir()
	cwd, binding := makeLinkedWorktreeFixture(t, dir)
	storeRoot := t.TempDir()
	// Seed only the existing binding — wsID below is different, so !found.
	makeBindings(t, storeRoot, []HerdrSpaceBinding{binding})

	// Override the predicate to call the real implementation with the
	// fixture cwd so the predicate actually PASSES.
	oldPred := herdrAutoCreatePredicateFn
	herdrAutoCreatePredicateFn = func(allBindings []HerdrSpaceBinding) bool {
		return herdrAutoCreatePredicateWith(cwd, allBindings, os.Stat, os.ReadFile)
	}
	t.Cleanup(func() { herdrAutoCreatePredicateFn = oldPred })

	// Stub auto-create to signal failure.
	autoCreateCalled := false
	oldCreate := herdrDefaultShellAutoCreateFn
	herdrDefaultShellAutoCreateFn = func(_ context.Context, _, _ string, _ string, _ io.Writer) (HerdrSpaceBinding, bool) {
		autoCreateCalled = true
		return HerdrSpaceBinding{}, false
	}
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = oldCreate })

	wsID := "wNEW" // not in bindings → !found path is taken
	getenv := func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return wsID
		case "SHELL":
			return "/bin/bash"
		}
		return ""
	}

	cap := &capturedExec{}
	// svc == nil: daemon unreachable, which is exactly when auto-create is
	// most likely to have failed.
	if err := herdrDefaultShellCore(context.Background(), getenv, storeRoot, nil, "/fake/nexus3", cap.fn); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !autoCreateCalled {
		t.Error("auto-create stub was never called; predicate may have returned false unexpectedly")
	}
	assertHostShell(t, cap, "/bin/bash")
}

// ── herdrAutoCreatePredicateWith unit tests ───────────────────────────────────

// makeLinkedWorktreeFixture creates a linked-worktree layout in dir:
//
//	<dir>/main/.git/           (directory — the main repo's .git)
//	<dir>/main/.git/worktrees/feat/  (worktree admin dir)
//	<dir>/worktree/.git        (file — the linked worktree's .git file)
//
// Returns (worktreePath, binding). The binding has a workspace ID matching
// the worktree so callers can seed it into the bindings file as required.
func makeLinkedWorktreeFixture(t *testing.T, dir string) (worktreePath string, binding HerdrSpaceBinding) {
	t.Helper()
	mainGit := filepath.Join(dir, "main", ".git")
	wtAdmin := filepath.Join(mainGit, "worktrees", "feat")
	if err := os.MkdirAll(wtAdmin, 0o755); err != nil {
		t.Fatal(err)
	}
	worktreePath = filepath.Join(dir, "worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	// The .git file in a linked worktree points at the worktrees admin dir.
	gitFile := filepath.Join(worktreePath, ".git")
	gitdirTarget := filepath.Join(mainGit, "worktrees", "feat")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+gitdirTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binding = HerdrSpaceBinding{
		SpaceLabel:       "nexus3:wt/feat",
		HerdrWorkspaceID: "wWT2",
		SandboxHandle:    "wt/feat",
		SandboxID:        "sb-wt2",
		RepoRoot:         filepath.Join(dir, "main"),
	}
	return worktreePath, binding
}

func TestHerdrAutoCreatePredicate_LinkedWorktreeWithBindings_Engages(t *testing.T) {
	// (b) cwd is inside a linked worktree AND (c) a binding exists with a
	// RepoRoot matching the linked worktree's main repo → predicate returns true.
	//
	// MUTATION PROOF (b deleted): replace the .git-file check with a check that
	// always returns true → main-checkout test fails. RED.
	//
	// MUTATION PROOF (c repo-comparison replaced with true): the
	// DifferentRepoRoot test below returns true. RED.
	//
	// MUTATION PROOF (len==0 fast-path deleted): linked worktree with NO
	// bindings (LinkedWorktreeNoBindings test) returns true. RED.
	dir := t.TempDir()
	cwd, binding := makeLinkedWorktreeFixture(t, dir)
	allBindings := []HerdrSpaceBinding{binding}

	got := herdrAutoCreatePredicateWith(cwd, allBindings, os.Stat, os.ReadFile)
	if !got {
		t.Error("predicate returned false for linked worktree with matching RepoRoot binding; want true")
	}
}

func TestHerdrAutoCreatePredicate_LinkedWorktreeNoBindings_DoesNotEngage(t *testing.T) {
	// (b) cwd is inside a linked worktree BUT (c) no bindings exist
	// → predicate returns false.
	//
	// MUTATION PROOF (c deleted): remove the len==0 fast-path → this returns
	// true → test fails RED.
	dir := t.TempDir()
	cwd, _ := makeLinkedWorktreeFixture(t, dir)

	got := herdrAutoCreatePredicateWith(cwd, nil, os.Stat, os.ReadFile)
	if got {
		t.Error("predicate returned true for linked worktree with no bindings; want false")
	}
}

func TestHerdrAutoCreatePredicate_MainCheckout_DoesNotEngage(t *testing.T) {
	// (b) cwd has a .git DIRECTORY (main checkout) → predicate returns false
	// regardless of bindings.
	//
	// What this proves: the predicate returns false when cwd is in a main
	// checkout (.git is a directory). It does NOT isolate the IsRegular()
	// guard — os.ReadFile on a .git DIRECTORY returns EISDIR, so :117
	// returns false regardless. The IsRegular() guard is still the right
	// mechanism; this test confirms the directory case is rejected, not
	// which exact line rejects it.
	dir := t.TempDir()
	mainGit := filepath.Join(dir, ".git")
	if err := os.MkdirAll(mainGit, 0o755); err != nil {
		t.Fatal(err)
	}
	allBindings := []HerdrSpaceBinding{{
		HerdrWorkspaceID: "wANY",
		SandboxHandle:    "any/sandbox",
	}}

	got := herdrAutoCreatePredicateWith(dir, allBindings, os.Stat, os.ReadFile)
	if got {
		t.Error("predicate returned true for a main checkout (.git is a directory); want false")
	}
}

func TestHerdrAutoCreatePredicate_NoGitFound_DoesNotEngage(t *testing.T) {
	// No .git anywhere in the walk → predicate returns false.
	dir := t.TempDir()
	allBindings := []HerdrSpaceBinding{{HerdrWorkspaceID: "wANY"}}

	got := herdrAutoCreatePredicateWith(dir, allBindings, os.Stat, os.ReadFile)
	if got {
		t.Error("predicate returned true when no .git exists; want false")
	}
}

// ── Additional herdrAutoCreatePredicateWith tests (repo-scoped predicate) ────

func TestHerdrAutoCreatePredicate_DifferentRepoRoot_DoesNotEngage(t *testing.T) {
	// Linked worktree of repo A, but the only binding belongs to repo B
	// (different RepoRoot) → predicate returns false.
	//
	// This is the exact defect the fix addresses: the old code returned true
	// whenever any binding existed, regardless of which repo it belonged to.
	// This test is RED against the pre-fix implementation.
	//
	// MUTATION PROOF (repo comparison replaced with true): this test fails RED.
	dir := t.TempDir()
	cwd, _ := makeLinkedWorktreeFixture(t, dir)

	// Binding whose RepoRoot points at an unrelated repo ("example-app").
	unrelatedBinding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:wt/other",
		HerdrWorkspaceID: "wOTH",
		SandboxHandle:    "wt/other",
		SandboxID:        "sb-other",
		RepoRoot:         "/some/unrelated/repo",
	}
	allBindings := []HerdrSpaceBinding{unrelatedBinding}

	got := herdrAutoCreatePredicateWith(cwd, allBindings, os.Stat, os.ReadFile)
	if got {
		t.Error("predicate returned true for linked worktree whose repo does not match any binding RepoRoot; want false")
	}
}

func TestHerdrAutoCreatePredicate_EmptyRepoRoot_LegacyBinding_DoesNotEngage(t *testing.T) {
	// Linked worktree with bindings that all have empty RepoRoot (legacy
	// bindings written before this field existed) → predicate returns false.
	// Empty RepoRoot must never act as a wildcard.
	//
	// MUTATION PROOF (empty RepoRoot treated as wildcard/match): this test
	// fails RED.
	dir := t.TempDir()
	cwd, _ := makeLinkedWorktreeFixture(t, dir)

	legacyBinding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:wt/legacy",
		HerdrWorkspaceID: "wLEG",
		SandboxHandle:    "wt/legacy",
		SandboxID:        "sb-leg",
		// RepoRoot intentionally absent (legacy binding).
	}
	allBindings := []HerdrSpaceBinding{legacyBinding}

	got := herdrAutoCreatePredicateWith(cwd, allBindings, os.Stat, os.ReadFile)
	if got {
		t.Error("predicate returned true for linked worktree when all bindings have empty RepoRoot; want false")
	}
}

func TestHerdrSpaceBinding_LegacyJSON_DecodesCleanly(t *testing.T) {
	// Verify that a binding JSON written before the repo_root field existed
	// decodes without error and produces an empty RepoRoot — encoding/json
	// leaves missing fields at their zero value.
	legacy := `[{"space_label":"nexus3:demo","herdr_workspace_id":"wXX","sandbox_handle":"demo","sandbox_id":"sb-demo"}]`
	var bindings []HerdrSpaceBinding
	if err := json.Unmarshal([]byte(legacy), &bindings); err != nil {
		t.Fatalf("unmarshal legacy binding: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("got %d bindings; want 1", len(bindings))
	}
	if bindings[0].RepoRoot != "" {
		t.Errorf("RepoRoot = %q; want empty string for legacy binding", bindings[0].RepoRoot)
	}
}

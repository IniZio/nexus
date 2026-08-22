package cli

import (
	"context"
	"encoding/json"
	"errors"
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

	// Sidecar must exist and line 1 must be a non-empty nexus3 binary path.
	// (Line 2 carries the kernel path and may be empty if unresolved at install.)
	sidecarData, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}
	lines := strings.SplitN(strings.TrimRight(string(sidecarData), "\n"), "\n", 2)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Error("sidecar line 1 (nexus3 binary path) is empty")
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
// Deleting the mechanism previously left the whole suite green, which is
// exactly why this test exists rather than relying on a live run.
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
			getenv := func(string) string { return tc.existing }

			herdrApplyKernelPath(tc.kernelPath, getenv, setenv)

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

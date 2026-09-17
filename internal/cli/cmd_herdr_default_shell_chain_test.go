package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = old
	w.Close()
	return <-done
}

func wsGetenv(wsID string, extra map[string]string) func(string) string {
	return func(k string) string {
		switch k {
		case "HERDR_WORKSPACE_ID":
			return wsID
		case "SHELL":
			return "/bin/bash"
		}
		return extra[k]
	}
}

func stubWtSeams(t *testing.T) *[]string {
	t.Helper()
	var childArgv []string
	oldChild := herdrWtChildRunnerFn
	herdrWtChildRunnerFn = func(_ context.Context, _ string, argv []string) error {
		childArgv = append([]string(nil), argv...)
		return nil
	}
	oldSpawn := herdrWtSpawnDetachedReapFn
	herdrWtSpawnDetachedReapFn = func(HerdrSpaceBinding) error { return nil }
	t.Cleanup(func() {
		herdrWtChildRunnerFn = oldChild
		herdrWtSpawnDetachedReapFn = oldSpawn
	})
	return &childArgv
}

func stubPredicate(t *testing.T, v bool) {
	t.Helper()
	old := herdrAutoCreatePredicateFn
	herdrAutoCreatePredicateFn = func([]HerdrSpaceBinding) bool { return v }
	t.Cleanup(func() { herdrAutoCreatePredicateFn = old })
}

func stubLinkedWorktreeReason(t *testing.T, v bool) {
	t.Helper()
	old := herdrLinkedWorktreeReasonFn
	herdrLinkedWorktreeReasonFn = func() bool { return v }
	t.Cleanup(func() { herdrLinkedWorktreeReasonFn = old })
}

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestHerdrDefaultShell_WorktreeSandboxBinding_ResolvesByWorkspaceID drives the
// real default-shell lookup with a binding written exactly the way the
// worktree-sandbox path (the path behind delegate_worktree_create and the
// worktree.created hook) writes it: through HerdrSpacePut, keyed on the herdr
// workspace id, labelled "nexus:<handle>". The live w18 record has this shape.
func TestHerdrDefaultShell_WorktreeSandboxBinding_ResolvesByWorkspaceID(t *testing.T) {
	storeRoot := t.TempDir()
	handle := "hanlun-lms/han-941-legacy-seed-verify"
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus:" + handle,
		HerdrWorkspaceID: "w18",
		SandboxHandle:    handle,
		SandboxID:        "sb-06GAFVM449VAB3SPJFV2Q0BM8M",
		GuestPaneID:      "w18:p5",
		RepoRoot:         "/home/newman/magic/hanlun-lms",
		WorktreeManaged:  true,
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, binding); err != nil {
		t.Fatal(err)
	}
	childArgv := stubWtSeams(t)
	stubPredicate(t, false)
	svc := &fakeDialableGetter{fakeDefaultShellGetter: fakeDefaultShellGetter{sb: domain.Sandbox{State: domain.Running}}}
	cap := &capturedExec{}
	next := writeExecutable(t, t.TempDir(), "other-guest-shell")
	getenv := wsGetenv("w18", map[string]string{herdrGuestShellNextEnv: next})

	if err := herdrDefaultShellCore(context.Background(), getenv, storeRoot, svc, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	if cap.calls != 0 {
		t.Fatalf("exec seam called (argv0=%q); a bound workspace must never chain or fall to the host shell", cap.argv0)
	}
	if svc.dialedRef != handle {
		t.Errorf("dialed %q, want %q", svc.dialedRef, handle)
	}
	joined := strings.Join(*childArgv, " ")
	if !strings.Contains(joined, " exec --pty ") || !strings.Contains(joined, " "+handle+" ") {
		t.Errorf("supervised child argv = %q; want nexus exec --pty ... %s", joined, handle)
	}
}

// TestHerdrDefaultShell_Unbound_ChainsToNextGuestShell: a workspace with no
// nexus binding hands the pane to the chained guest shell, not to $SHELL.
func TestHerdrDefaultShell_Unbound_ChainsToNextGuestShell(t *testing.T) {
	storeRoot := t.TempDir()
	makeBindings(t, storeRoot, []HerdrSpaceBinding{testBinding})
	stubPredicate(t, false)
	stubLinkedWorktreeReason(t, false)
	next := writeExecutable(t, t.TempDir(), "herdr-plugin-msb-guest-shell")
	cap := &capturedExec{}

	err := herdrDefaultShellCore(context.Background(), wsGetenv("wOTHER", map[string]string{herdrGuestShellNextEnv: next}), storeRoot, nil, "/fake/nexus", cap.fn)
	if err != nil {
		t.Fatal(err)
	}
	if cap.calls != 1 || cap.argv0 != next {
		t.Fatalf("exec argv0 = %q (calls=%d), want chained guest shell %q", cap.argv0, cap.calls, next)
	}
}

func TestHerdrDefaultShell_Unbound_NoWorkspaceID_Chains(t *testing.T) {
	next := writeExecutable(t, t.TempDir(), "other")
	cap := &capturedExec{}
	if err := herdrDefaultShellCore(context.Background(), wsGetenv("", map[string]string{herdrGuestShellNextEnv: next}), t.TempDir(), nil, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	if cap.argv0 != next {
		t.Fatalf("argv0 = %q, want %q", cap.argv0, next)
	}
}

func TestHerdrDefaultShell_Unbound_NextMissingOrSelf_HostShell(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, next := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "nope"),
		"self":    self,
		"dir":     t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			stubPredicate(t, false)
			stubLinkedWorktreeReason(t, false)
			cap := &capturedExec{}
			if err := herdrDefaultShellCore(context.Background(), wsGetenv("wX", map[string]string{herdrGuestShellNextEnv: next}), t.TempDir(), nil, "/fake/nexus", cap.fn); err != nil {
				t.Fatal(err)
			}
			assertHostShell(t, cap, "/bin/bash")
		})
	}
}

func TestHerdrDefaultShell_HostShellEscapeHatch_NeverChains(t *testing.T) {
	next := writeExecutable(t, t.TempDir(), "other")
	cap := &capturedExec{}
	getenv := wsGetenv("wX", map[string]string{herdrGuestShellNextEnv: next, "NEXUS_HOST_SHELL": "1"})
	if err := herdrDefaultShellCore(context.Background(), getenv, t.TempDir(), nil, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// A bound workspace whose sandbox is not running is a nexus failure: fall to
// the host shell with nexus's own diagnostics, never into another plugin's.
func TestHerdrDefaultShell_BoundButNotRunning_NeverChains(t *testing.T) {
	storeRoot := t.TempDir()
	makeBindings(t, storeRoot, []HerdrSpaceBinding{testBinding})
	next := writeExecutable(t, t.TempDir(), "other")
	svc := &fakeDefaultShellGetter{sb: domain.Sandbox{State: domain.Stopped}}
	cap := &capturedExec{}
	if err := herdrDefaultShellCore(context.Background(), wsGetenv(testBinding.HerdrWorkspaceID, map[string]string{herdrGuestShellNextEnv: next}), storeRoot, svc, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	assertHostShell(t, cap, "/bin/bash")
}

// The first tab of a linked worktree that auto-create does not engage for must
// say why on the pane instead of silently opening a host shell.
func TestHerdrDefaultShell_LinkedWorktreeNotEngaged_PrintsReason(t *testing.T) {
	stubPredicate(t, false)
	stubLinkedWorktreeReason(t, true)
	cap := &capturedExec{}
	out := captureStderr(t, func() {
		if err := herdrDefaultShellCore(context.Background(), wsGetenv("w7", nil), t.TempDir(), nil, "/fake/nexus", cap.fn); err != nil {
			t.Error(err)
		}
	})
	assertHostShell(t, cap, "/bin/bash")
	if !strings.Contains(out, "workspace w7 has no nexus sandbox binding") {
		t.Errorf("stderr = %q; want a one-line reason naming workspace w7", out)
	}
}

func TestHerdrDefaultShell_NonWorktreeNotEngaged_Silent(t *testing.T) {
	stubPredicate(t, false)
	stubLinkedWorktreeReason(t, false)
	cap := &capturedExec{}
	out := captureStderr(t, func() {
		if err := herdrDefaultShellCore(context.Background(), wsGetenv("w7", nil), t.TempDir(), nil, "/fake/nexus", cap.fn); err != nil {
			t.Error(err)
		}
	})
	assertHostShell(t, cap, "/bin/bash")
	if out != "" {
		t.Errorf("stderr = %q; a plain host workspace must stay silent", out)
	}
}

func TestHerdrDefaultShell_AutoCreateFails_PrintsReason(t *testing.T) {
	stubPredicate(t, true)
	old := herdrDefaultShellAutoCreateFn
	herdrDefaultShellAutoCreateFn = func(context.Context, string, string, string, io.Writer) (HerdrSpaceBinding, bool) {
		return HerdrSpaceBinding{}, false
	}
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = old })
	cap := &capturedExec{}
	out := captureStderr(t, func() {
		if err := herdrDefaultShellCore(context.Background(), wsGetenv("w9", nil), t.TempDir(), nil, "/fake/nexus", cap.fn); err != nil {
			t.Error(err)
		}
	})
	assertHostShell(t, cap, "/bin/bash")
	if !strings.Contains(out, "workspace w9 still has no sandbox binding") || !strings.Contains(out, "space-open-pane w9") {
		t.Errorf("stderr = %q; want the retry hint naming w9", out)
	}
}

// The first tab races the worktree.created hook pane for the same handle and
// may wait the full create-intent lock timeout; the outer bound must cover it.
func TestHerdrAutoCreateTimeout_CoversCreateLockWait(t *testing.T) {
	if herdrAutoCreateTimeout <= herdrWorktreeCreateLockTimeout {
		t.Fatalf("herdrAutoCreateTimeout=%s must exceed herdrWorktreeCreateLockTimeout=%s, or the first tab is killed while the sibling build is still succeeding", herdrAutoCreateTimeout, herdrWorktreeCreateLockTimeout)
	}
}

// Predicate (c) must match worktree-sandbox --auto: an onboarded checkout
// engages even when the repo has no bound sibling yet.
func TestHerdrAutoCreatePredicate_OnboardedWorktreeNoBindings_Engages(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := makeLinkedWorktreeFixture(t, dir)
	if err := os.MkdirAll(filepath.Join(cwd, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".nexus", "config.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !herdrAutoCreatePredicateWith(filepath.Join(cwd, "sub"), nil, os.Stat, os.ReadFile) {
		t.Fatal("predicate = false for an onboarded linked worktree with no bindings; want true (hook provisions it, first tab must wait for it)")
	}
}

func TestHerdrParseSidecar_ThirdLineIsNextShell(t *testing.T) {
	bin, kernel, next := herdrParseSidecar([]byte("/usr/bin/nexus\n/k/vmlinux\n/usr/bin/msb-guest-shell\n"))
	if bin != "/usr/bin/nexus" || kernel != "/k/vmlinux" || next != "/usr/bin/msb-guest-shell" {
		t.Fatalf("got (%q,%q,%q)", bin, kernel, next)
	}
	bin, kernel, next = herdrParseSidecar([]byte("/usr/bin/nexus\n\n"))
	if bin != "/usr/bin/nexus" || kernel != "" || next != "" {
		t.Fatalf("two-line sidecar: got (%q,%q,%q)", bin, kernel, next)
	}
}

func TestHerdrApplyGuestShellNext(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	setenv := func(k, v string) error { env[k] = v; return nil }
	herdrApplyGuestShellNext("/stamped", getenv, setenv)
	if env[herdrGuestShellNextEnv] != "/stamped" {
		t.Fatalf("stamped value not published: %v", env)
	}
	herdrApplyGuestShellNext("/other", getenv, setenv)
	if env[herdrGuestShellNextEnv] != "/stamped" {
		t.Fatalf("explicit env must win: %v", env)
	}
}

func TestHerdrInstallDefaultShellParseArgs(t *testing.T) {
	exe := writeExecutable(t, t.TempDir(), "msb-guest-shell")
	if got, wc, err := herdrInstallDefaultShellParseArgs([]string{"--next", exe}); err != nil || got != exe || wc {
		t.Fatalf("--next: got (%q, %v, %v)", got, wc, err)
	}
	if got, wc, err := herdrInstallDefaultShellParseArgs([]string{"--next=" + exe}); err != nil || got != exe || wc {
		t.Fatalf("--next=: got (%q, %v, %v)", got, wc, err)
	}
	if got, wc, err := herdrInstallDefaultShellParseArgs(nil); err != nil || got != "" || wc {
		t.Fatalf("no args: got (%q, %v, %v)", got, wc, err)
	}
	if _, wc, err := herdrInstallDefaultShellParseArgs([]string{"--write-config"}); err != nil || !wc {
		t.Fatalf("--write-config: got (wc=%v, %v)", wc, err)
	}
	for name, args := range map[string][]string{
		"missing":  {"--next", filepath.Join(t.TempDir(), "nope")},
		"self":     {"--next", filepath.Join(t.TempDir(), "nexus-guest-shell")},
		"unknown":  {"--bogus"},
		"dangling": {"--next"},
	} {
		if _, _, err := herdrInstallDefaultShellParseArgs(args); err == nil {
			t.Errorf("%s: want error for %v", name, args)
		}
	}
}

func TestHerdrInstallDefaultShell_StampsNextShell(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if !herdrSkipInstallProbeForTest {
		t.Fatal("testmain_test.go must set herdrSkipInstallProbeForTest; the probe would re-run the test binary")
	}
	next := writeExecutable(t, t.TempDir(), "msb-guest-shell")
	var sb strings.Builder
	if err := runHerdrInstallDefaultShell(context.Background(), []string{"--next", next}, &Output{w: &sb}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "bin", "nexus-guest-shell"+herdrSidecarSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, got := herdrParseSidecar(data); got != next {
		t.Fatalf("sidecar line 3 = %q, want %q (sidecar=%q)", got, next, data)
	}
	if !strings.Contains(sb.String(), next) {
		t.Errorf("install output should name the chained shell: %q", sb.String())
	}
}

// ── argv + cwd pass-through (FW-TAB-GATE-UNBOUND) ────────────────────────────

var paneShellArgs = []string{"-c", "echo SHELL_REACHED; pwd"}

func stubShellArgs(t *testing.T, args ...string) {
	t.Helper()
	old := herdrGuestShellArgsFn
	herdrGuestShellArgsFn = func() []string { return append([]string(nil), args...) }
	t.Cleanup(func() { herdrGuestShellArgsFn = old })
}

// paneCwd moves the test into a fresh directory that plays the pane's cwd, so
// the pass-through assertion has a known value independent of test order.
func paneCwd(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

type cwdExec struct {
	capturedExec
	cwdAtExec string
}

func (c *cwdExec) fn(argv0 string, argv []string, envv []string) error {
	c.cwdAtExec, _ = os.Getwd()
	return c.capturedExec.fn(argv0, argv, envv)
}

func assertArgvAndCwd(t *testing.T, cap *cwdExec, wantArgv0 string, wantCwd string) {
	t.Helper()
	if cap.calls != 1 {
		t.Fatalf("exec called %d times, want 1", cap.calls)
	}
	wantArgv := append([]string{wantArgv0}, paneShellArgs...)
	if cap.argv0 != wantArgv0 || !reflect.DeepEqual(cap.argv, wantArgv) {
		t.Errorf("exec argv0=%q argv=%q; want argv0=%q argv=%q (pane shell args dropped?)", cap.argv0, cap.argv, wantArgv0, wantArgv)
	}
	if cap.cwdAtExec != wantCwd {
		t.Errorf("cwd at exec = %q, want the pane cwd %q", cap.cwdAtExec, wantCwd)
	}
}

// Case 1: label-lookup miss on a main checkout — the live w2/w1F shape.
func TestHerdrDefaultShell_UnboundMainCheckout_ChainsSilentlyWithArgvAndCwd(t *testing.T) {
	storeRoot := t.TempDir()
	makeBindings(t, storeRoot, []HerdrSpaceBinding{testBinding})
	stubPredicate(t, false)
	stubLinkedWorktreeReason(t, false)
	stubShellArgs(t, paneShellArgs...)
	next := writeExecutable(t, t.TempDir(), "herdr-plugin-msb-guest-shell")
	cap := &cwdExec{}
	wantCwd := paneCwd(t)

	var err error
	stderr := captureStderr(t, func() {
		err = herdrDefaultShellCore(context.Background(), wsGetenv("w2", map[string]string{herdrGuestShellNextEnv: next}), storeRoot, nil, "/fake/nexus", cap.fn)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("unbound main checkout must chain silently; stderr = %q", stderr)
	}
	assertArgvAndCwd(t, cap, next, wantCwd)
}

func TestHerdrDefaultShell_UnboundMainCheckout_HostShellKeepsArgvAndCwd(t *testing.T) {
	storeRoot := t.TempDir()
	makeBindings(t, storeRoot, []HerdrSpaceBinding{testBinding})
	stubPredicate(t, false)
	stubLinkedWorktreeReason(t, false)
	stubShellArgs(t, paneShellArgs...)
	cap := &cwdExec{}
	wantCwd := paneCwd(t)

	if err := herdrDefaultShellCore(context.Background(), wsGetenv("w2", nil), storeRoot, nil, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	assertArgvAndCwd(t, cap, "/bin/bash", wantCwd)
}

// Case 2: bound workspace enters the guest; the chain is never consulted and
// the guest argv keeps its own shape.
func TestHerdrDefaultShell_Bound_EntersGuest_NeverChains(t *testing.T) {
	storeRoot := t.TempDir()
	makeBindings(t, storeRoot, []HerdrSpaceBinding{testBinding})
	stubPredicate(t, false)
	stubLinkedWorktreeReason(t, false)
	stubShellArgs(t, paneShellArgs...)
	next := writeExecutable(t, t.TempDir(), "herdr-plugin-msb-guest-shell")
	svc := &fakeDialableGetter{fakeDefaultShellGetter: fakeDefaultShellGetter{sb: domain.Sandbox{State: domain.Running}}}
	cap := &cwdExec{}

	if err := herdrDefaultShellCore(context.Background(), wsGetenv(testBinding.HerdrWorkspaceID, map[string]string{herdrGuestShellNextEnv: next}), storeRoot, svc, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	if cap.calls != 1 || cap.argv0 != "/fake/nexus" {
		t.Fatalf("exec argv0 = %q (calls=%d), want the nexus guest exec", cap.argv0, cap.calls)
	}
	want := []string{"/fake/nexus", "exec", "--pty", "--cwd", "/root", testBinding.SandboxHandle, "/bin/bash", "--login"}
	if !reflect.DeepEqual(cap.argv, want) {
		t.Errorf("guest argv = %q, want %q", cap.argv, want)
	}
}

// Case 3: linked worktree without a binding auto-creates (TAB-GATE) and then
// enters the guest through the supervised child; no chain, no host shell.
func TestHerdrDefaultShell_UnboundLinkedWorktree_AutoCreatesThenGuest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wsID := "wWT2"
	binding := HerdrSpaceBinding{SpaceLabel: "nexus:wt/feat", HerdrWorkspaceID: wsID, SandboxHandle: "wt/feat", SandboxID: "sb-wt2", WorktreeManaged: true}
	stubPredicate(t, true)
	stubShellArgs(t, paneShellArgs...)
	childArgv := stubWtSeams(t)
	autoCreated := false
	old := herdrDefaultShellAutoCreateFn
	herdrDefaultShellAutoCreateFn = func(_ context.Context, _, gotWsID, _ string, _ io.Writer) (HerdrSpaceBinding, bool) {
		autoCreated = gotWsID == wsID
		return binding, true
	}
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = old })
	next := writeExecutable(t, t.TempDir(), "herdr-plugin-msb-guest-shell")
	svc := &fakeDialableGetter{fakeDefaultShellGetter: fakeDefaultShellGetter{sb: domain.Sandbox{State: domain.Running}}}
	cap := &cwdExec{}

	if err := herdrDefaultShellCore(context.Background(), wsGetenv(wsID, map[string]string{herdrGuestShellNextEnv: next}), t.TempDir(), svc, "/fake/nexus", cap.fn); err != nil {
		t.Fatal(err)
	}
	if !autoCreated {
		t.Fatal("auto-create was not invoked for the linked worktree")
	}
	if cap.calls != 0 {
		t.Fatalf("exec seam called (argv0=%q); auto-created worktree must enter the guest, not chain", cap.argv0)
	}
	joined := strings.Join(*childArgv, " ")
	if !strings.Contains(joined, " exec --pty ") || !strings.Contains(joined, " wt/feat ") {
		t.Errorf("supervised child argv = %q; want nexus exec --pty ... wt/feat", joined)
	}
}

func TestHerdrGuestShellArgs_DefaultFollowsArgv0Dispatch(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	os.Args = append([]string{"/x/nexus-guest-shell"}, paneShellArgs...)
	if got := herdrGuestShellArgsFn(); !reflect.DeepEqual(got, paneShellArgs) {
		t.Errorf("guest-shell dispatch args = %q, want %q", got, paneShellArgs)
	}
	os.Args = []string{"/x/nexus", "herdr", "default-shell"}
	if got := herdrGuestShellArgsFn(); len(got) != 0 {
		t.Errorf("CLI verb args = %q, want none", got)
	}
}

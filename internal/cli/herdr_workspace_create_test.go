package cli

// Tests for the stray-tab defect fix and its revision: herdrWorkspaceCreate
// threading a host cwd through to `herdr workspace create --cwd`, and
// grafting the guest pane onto the workspace's root pane (--target-pane)
// instead of opening a second tab and then closing the first one.
//
// SAFETY: every test here replaces herdrExecCommandContext with a fake that
// only ever shells out to /bin/true, /bin/false-equivalent shell snippets, or
// printf — never to a real herdr binary. HERDR_BIN_PATH is set to a
// placeholder path that is never actually executed (resolveHerdrBin does not
// stat it), so these tests cannot create, modify, close, or focus a live
// herdr workspace.

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// fakeHerdrExec installs a fake herdrExecCommandContext for the duration of
// the test. Every invocation is recorded, in order, into *calls as
// []string{name, args...}; respond decides what (harmless, local) command
// actually runs to produce the exit code / stdout the caller sees.
func fakeHerdrExec(t *testing.T, calls *[][]string, respond func(args []string) *exec.Cmd) {
	t.Helper()
	orig := herdrExecCommandContext
	t.Cleanup(func() { herdrExecCommandContext = orig })
	herdrExecCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		full := append([]string{name}, args...)
		*calls = append(*calls, full)
		return respond(args)
	}
}

// fakeWorkspaceCreateCmd returns a command that prints a canned "workspace
// create" JSON envelope to stdout, standing in for a real herdr invocation.
func fakeWorkspaceCreateCmd(workspaceID, rootPaneID string) *exec.Cmd {
	body := fmt.Sprintf(`{"result":{"workspace":{"workspace_id":%q},"root_pane":{"pane_id":%q},"tab":{"tab_id":"t1"}}}`, workspaceID, rootPaneID)
	return exec.Command("printf", "%s", body)
}

func containsAdjacent(argv []string, a, b string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == a && argv[i+1] == b {
			return true
		}
	}
	return false
}

func contains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

// ── herdrWorkspaceCreate: --cwd threading, root pane id parsing ─────────────

func TestHerdrWorkspaceCreate_PassesCwdFlagWhenKnown(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		return fakeWorkspaceCreateCmd("wF", "p1")
	})

	wsID, rootPaneID, err := herdrWorkspaceCreate(context.Background(), "/fake/herdr", "nexus3:proj/x", "/home/me/proj-x")
	if err != nil {
		t.Fatalf("herdrWorkspaceCreate: %v", err)
	}
	if wsID != "wF" {
		t.Errorf("workspace ID = %q, want wF", wsID)
	}
	if rootPaneID != "p1" {
		t.Errorf("root pane ID = %q, want p1 — must be parsed from the create envelope", rootPaneID)
	}

	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	argv := calls[0]
	if !containsAdjacent(argv, "--cwd", "/home/me/proj-x") {
		t.Errorf("argv %v does not contain \"--cwd\" \"/home/me/proj-x\"", argv)
	}
}

func TestHerdrWorkspaceCreate_OmitsCwdFlagWhenUnknown(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		return fakeWorkspaceCreateCmd("wQ", "p9")
	})

	if _, _, err := herdrWorkspaceCreate(context.Background(), "/fake/herdr", "nexus3:proj/x", ""); err != nil {
		t.Fatalf("herdrWorkspaceCreate: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if contains(calls[0], "--cwd") {
		t.Errorf("argv %v contains --cwd with no known host path; must be omitted, not passed empty", calls[0])
	}
}

func TestHerdrWorkspaceCreate_PlainTextFallbackHasNoRootPaneID(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		// Not JSON — herdr running in plain-text mode.
		return exec.Command("printf", "%s", "wPlain")
	})

	wsID, rootPaneID, err := herdrWorkspaceCreate(context.Background(), "/fake/herdr", "nexus3:proj/x", "")
	if err != nil {
		t.Fatalf("herdrWorkspaceCreate: %v", err)
	}
	if wsID != "wPlain" {
		t.Errorf("workspace ID = %q, want wPlain", wsID)
	}
	if rootPaneID != "" {
		t.Errorf("root pane ID = %q, want empty — not recoverable in plain-text mode", rootPaneID)
	}
}

// ── herdrShellHostCwd ─────────────────────────────────────────────────────────

func TestHerdrShellHostCwd_UsesFirstLiveMountHostPath(t *testing.T) {
	sb := domain.Sandbox{
		LiveMounts: []domain.LiveMount{
			{HostPath: "$HOME/proj-a", GuestPath: "/work"},
			{HostPath: "$HOME/proj-b", GuestPath: "/work2"},
		},
	}
	got := herdrShellHostCwd(context.Background(), "ref", &fakeSandboxGetter{sb: sb})
	if got != "$HOME/proj-a" {
		t.Errorf("got %q, want $HOME/proj-a", got)
	}
}

func TestHerdrShellHostCwd_EmptyWhenNoLiveMount(t *testing.T) {
	cases := []struct {
		name string
		sb   domain.Sandbox
	}{
		{"no mounts at all", domain.Sandbox{}},
		{
			"only a mounted volume (no host path exists for volumes)",
			domain.Sandbox{MountedVolumes: []domain.VolumeAttachment{{Name: "v", GuestPath: "/mnt/v"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := herdrShellHostCwd(context.Background(), "ref", &fakeSandboxGetter{sb: tc.sb})
			if got != "" {
				t.Errorf("got %q, want empty so the caller omits --cwd and lets herdr default", got)
			}
		})
	}
}

func TestHerdrShellHostCwd_ServiceErrorReturnsEmpty(t *testing.T) {
	got := herdrShellHostCwd(context.Background(), "ref", &fakeSandboxGetter{err: fmt.Errorf("boom")})
	if got != "" {
		t.Errorf("got %q, want empty on service error", got)
	}
}

// ── herdrOpenGuestShellPane: graft vs. fallback argv shape ───────────────────

func TestHerdrOpenGuestShellPane_GraftsOntoRootPaneWhenKnown(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return exec.Command("/bin/true") })

	if err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1"); err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	argv := calls[0]
	if !containsAdjacent(argv, "--placement", "split") {
		t.Errorf("argv %v missing --placement split", argv)
	}
	if !containsAdjacent(argv, "--target-pane", "p1") {
		t.Errorf("argv %v missing --target-pane p1", argv)
	}
	if !containsAdjacent(argv, "--direction", "right") {
		t.Errorf("argv %v missing --direction right", argv)
	}
	if contains(argv, "--workspace") {
		t.Errorf("argv %v must not contain --workspace when --target-pane is used — herdr rejects the combination", argv)
	}
}

func TestHerdrOpenGuestShellPane_FallsBackToWorkspaceWhenNoRootPaneID(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return exec.Command("/bin/true") })

	if err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", ""); err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	argv := calls[0]
	if !containsAdjacent(argv, "--workspace", "wF") {
		t.Errorf("argv %v missing --workspace wF fallback", argv)
	}
	if contains(argv, "--target-pane") {
		t.Errorf("argv %v must not contain --target-pane with no known root pane id", argv)
	}
}

// ── space-open-pane: graft-not-close behaviour, end to end ──────────────────

// TestHerdrPluginSpaceOpenPane_GraftsGuestPaneOntoRootPane drives the real
// adopt-and-mint path (no existing binding) and asserts the full observable
// argv sequence: workspace create (carrying --cwd), then plugin pane open
// carrying --placement split / --target-pane <root-pane-id> / --direction
// right — and, critically, that `tab close` is never invoked at all. An
// earlier version of this fix closed the root tab after opening a second one;
// that SIGHUPs whatever is running in it, which is exactly what this
// revision replaces.
func TestHerdrPluginSpaceOpenPane_GraftsGuestPaneOntoRootPane(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Project: "proj", Name: "x", State: domain.Running,
		LiveMounts: []domain.LiveMount{{HostPath: "$HOME/proj-x", GuestPath: "/work"}},
	}
	g := &fakeAdoptGetter{byRef: map[string]domain.Sandbox{sb.Handle(): sb}}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		if len(args) >= 2 && args[0] == "workspace" && args[1] == "create" {
			return fakeWorkspaceCreateCmd("wF", "p1")
		}
		return exec.Command("/bin/true")
	})

	if err := herdrPluginSpaceOpenPane(ctx, sb.Handle(), storeRoot, g); err != nil {
		t.Fatalf("space-open-pane: %v", err)
	}

	var createIdx, paneOpenIdx = -1, -1
	for i, argv := range calls {
		switch {
		case len(argv) >= 3 && argv[1] == "workspace" && argv[2] == "create":
			createIdx = i
			if !containsAdjacent(argv, "--cwd", "$HOME/proj-x") {
				t.Errorf("workspace create argv %v missing --cwd $HOME/proj-x", argv)
			}
		case len(argv) >= 3 && argv[1] == "plugin" && argv[2] == "pane":
			paneOpenIdx = i
			if !containsAdjacent(argv, "--target-pane", "p1") {
				t.Errorf("pane open argv %v missing --target-pane p1", argv)
			}
			if !containsAdjacent(argv, "--placement", "split") {
				t.Errorf("pane open argv %v missing --placement split", argv)
			}
			if !containsAdjacent(argv, "--direction", "right") {
				t.Errorf("pane open argv %v missing --direction right", argv)
			}
			if contains(argv, "--workspace") {
				t.Errorf("pane open argv %v must not contain --workspace alongside --target-pane", argv)
			}
		case len(argv) >= 2 && argv[1] == "tab":
			t.Errorf("tab close was called (%v) — this revision must never close a tab", argv)
		}
	}

	if createIdx == -1 {
		t.Fatal("workspace create was never called")
	}
	if paneOpenIdx == -1 {
		t.Fatal("plugin pane open was never called")
	}
	if !(createIdx < paneOpenIdx) {
		t.Errorf("expected order create(%d) < pane-open(%d)", createIdx, paneOpenIdx)
	}
}

// TestHerdrPluginSpaceOpenPane_ReusedWorkspaceOpensPaneWithoutCreating asserts
// that when an existing binding already carries a workspace ID, no new
// workspace is created, the pane is opened via the --workspace fallback (no
// root pane id is known for a reused workspace), and — as always — no tab is
// ever closed.
func TestHerdrPluginSpaceOpenPane_ReusedWorkspaceOpensPaneWithoutCreating(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/z", HerdrWorkspaceID: "wExisting",
		SandboxHandle: "proj/z", SandboxID: "sb-z",
	}
	if err := HerdrSpacePut(ctx, storeRoot, b); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	g := &fakeAdoptGetter{}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		return exec.Command("/bin/true")
	})

	if err := herdrPluginSpaceOpenPane(ctx, "proj/z", storeRoot, g); err != nil {
		t.Fatalf("space-open-pane: %v", err)
	}

	paneOpened := false
	for _, argv := range calls {
		if len(argv) >= 3 && argv[1] == "workspace" && argv[2] == "create" {
			t.Errorf("workspace create called for an already-bound space: %v", argv)
		}
		if len(argv) >= 2 && argv[1] == "tab" {
			t.Errorf("tab close called — this revision must never close a tab: %v", argv)
		}
		if len(argv) >= 3 && argv[1] == "plugin" && argv[2] == "pane" {
			paneOpened = true
			if !containsAdjacent(argv, "--workspace", "wExisting") {
				t.Errorf("pane open argv %v missing --workspace wExisting fallback", argv)
			}
		}
	}
	if !paneOpened {
		t.Error("plugin pane open was never called")
	}
}

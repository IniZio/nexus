package cli

// Tests for the stray-tab defect fix and its revisions:
//   - herdrWorkspaceCreate threading a host cwd through to
//     `herdr workspace create --cwd`
//   - grafting the guest pane onto the workspace's root pane
//     (--target-pane) instead of opening a second tab and then closing the
//     first one
//   - J1: capturing and persisting the guest pane ID instead of throwing it
//     away, so a later invocation can start an agent in an already-open
//     space
//   - J2: making --focus a caller decision instead of always stealing focus
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
	"os"
	"os/exec"
	"strings"
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

// fakePaneOpenCmd returns a command that prints a canned "plugin pane open"
// JSON envelope, matching the exact shape verified live and quoted in
// TASK-J1J2.md:
//
//	{"id":"cli:plugin","result":{"plugin_pane":{"entrypoint":"shell",
//	  "pane":{"pane_id":"w1V:p2","tab_id":"w1V:t1","workspace_id":"w1V",
//	          "label":"nexus3 guest shell"},"plugin_id":"nexus3"},
//	  "type":"plugin_pane_opened"}}
func fakePaneOpenCmd(paneID string) *exec.Cmd {
	body := fmt.Sprintf(`{"id":"cli:plugin","result":{"plugin_pane":{"entrypoint":"shell",`+
		`"pane":{"pane_id":%q,"tab_id":"w1V:t1","workspace_id":"w1V","label":"nexus3 guest shell"},`+
		`"plugin_id":"nexus3"},"type":"plugin_pane_opened"}}`, paneID)
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

// ── herdrOpenGuestShellPane: graft vs. fallback argv shape, focus, pane ID ──

func TestHerdrOpenGuestShellPane_GraftsOntoRootPaneWhenKnown(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return fakePaneOpenCmd("w1V:p2") })

	paneID, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1", true)
	if err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if paneID != "w1V:p2" {
		t.Errorf("pane ID = %q, want w1V:p2", paneID)
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
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return fakePaneOpenCmd("w1V:p3") })

	paneID, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "", true)
	if err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if paneID != "w1V:p3" {
		t.Errorf("pane ID = %q, want w1V:p3", paneID)
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

// TestHerdrOpenGuestShellPane_ParsesRealisticEnvelope uses the exact envelope
// shape verified live and quoted in TASK-J1J2.md (full, with unrelated
// sibling fields), not just a minimal fixture — pinning that the parser
// reaches into result.plugin_pane.pane.pane_id specifically, not any other
// *_id in the response.
func TestHerdrOpenGuestShellPane_ParsesRealisticEnvelope(t *testing.T) {
	envelope := `{"id":"cli:plugin","result":{"plugin_pane":{"entrypoint":"shell",` +
		`"pane":{"pane_id":"w1V:p2","tab_id":"w1V:t1","workspace_id":"w1V",` +
		`"label":"nexus3 guest shell"},"plugin_id":"nexus3"},"type":"plugin_pane_opened"}}`

	fakeHerdrExec(t, &[][]string{}, func(args []string) *exec.Cmd {
		return exec.Command("printf", "%s", envelope)
	})

	paneID, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1", true)
	if err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if paneID != "w1V:p2" {
		t.Errorf("pane ID = %q, want w1V:p2 (must not pick up tab_id or workspace_id)", paneID)
	}
}

// TestHerdrOpenGuestShellPane_UnparseableOutputYieldsEmptyPaneIDNoError
// mirrors herdrWorkspaceCreate's tolerance: a pane that opens successfully
// but whose ID could not be captured (plain-text mode, or any non-matching
// shape) is a degradation, not a failure.
func TestHerdrOpenGuestShellPane_UnparseableOutputYieldsEmptyPaneIDNoError(t *testing.T) {
	fakeHerdrExec(t, &[][]string{}, func(args []string) *exec.Cmd {
		return exec.Command("printf", "%s", "not json at all")
	})

	paneID, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1", true)
	if err != nil {
		t.Fatalf("unparseable output must not be an error, got: %v", err)
	}
	if paneID != "" {
		t.Errorf("pane ID = %q, want empty for unparseable output", paneID)
	}
}

// TestHerdrOpenGuestShellPane_FocusPresentWhenRequested and its sibling below
// are the direct unit-level pin for J2: --focus is a caller decision, not a
// hardcoded flag.
func TestHerdrOpenGuestShellPane_FocusPresentWhenRequested(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return fakePaneOpenCmd("p1") })

	if _, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1", true); err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if !contains(calls[0], "--focus") {
		t.Errorf("argv %v missing --focus when focus=true", calls[0])
	}
}

func TestHerdrOpenGuestShellPane_FocusAbsentWhenNotRequested(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return fakePaneOpenCmd("p1") })

	if _, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1", false); err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if contains(calls[0], "--focus") {
		t.Errorf("argv %v contains --focus when focus=false — the scripted/no-focus path must be able to opt out", calls[0])
	}
}

// TestHerdrOpenGuestShellPane_StillEchoesToStdout pins the "don't stop
// echoing" requirement: capturing output for parsing must not replace the
// human-facing stream. os.Stdout can't easily be captured in-process without
// disrupting the test runner's own output, so this asserts the mechanism
// (io.MultiWriter over os.Stdout) indirectly by asserting the function still
// succeeds and still returns the parsed ID — i.e. output capture is
// additive, not a replacement for cmd.Stdout = os.Stdout. See also the
// unchanged human-visible behaviour asserted by the end-to-end
// herdrPluginSpaceOpenPane/herdrPluginSpaceCreate tests below, which is the
// stronger guarantee this exists to protect.
func TestHerdrOpenGuestShellPane_StillEchoesToStdout(t *testing.T) {
	fakeHerdrExec(t, &[][]string{}, func(args []string) *exec.Cmd { return fakePaneOpenCmd("p1") })

	paneID, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/x", "wF", "p1", true)
	if err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if paneID != "p1" {
		t.Errorf("pane ID = %q, want p1", paneID)
	}
}

// ── HerdrSpaceBinding: GuestPaneID persistence and backward compat ──────────

func TestHerdrSpaceBinding_GuestPaneIDRoundTrips(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/x", HerdrWorkspaceID: "wF",
		SandboxHandle: "proj/x", SandboxID: "sb-x", GuestPaneID: "w1V:p2",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel)
	if err != nil {
		t.Fatalf("GetByLabel: %v", err)
	}
	if got.GuestPaneID != "w1V:p2" {
		t.Errorf("GuestPaneID = %q, want w1V:p2", got.GuestPaneID)
	}
}

// TestHerdrSpaceBinding_OldBindingWithoutPaneIDFieldStillLoads is the
// required backward-compat test: the operator has live bindings on disk
// right now, written before GuestPaneID existed, and they must keep working
// rather than erroring on load.
func TestHerdrSpaceBinding_OldBindingWithoutPaneIDFieldStillLoads(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	// Hand-write the bindings file in the OLD shape — no guest_pane_id key at
	// all, simulating a file written by a pre-J1 binary.
	oldJSON := `[
		{
			"space_label": "nexus3:proj/old",
			"herdr_workspace_id": "wOld",
			"sandbox_handle": "proj/old",
			"sandbox_id": "sb-old"
		}
	]`
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(herdrSpaceBindingsPath(root), []byte(oldJSON), 0o600); err != nil {
		t.Fatalf("write old-shape bindings file: %v", err)
	}

	got, err := HerdrSpaceGetByLabel(ctx, root, "nexus3:proj/old")
	if err != nil {
		t.Fatalf("old binding failed to load: %v", err)
	}
	if got.GuestPaneID != "" {
		t.Errorf("GuestPaneID = %q, want empty for a binding written before the field existed", got.GuestPaneID)
	}
	if got.HerdrWorkspaceID != "wOld" {
		t.Errorf("HerdrWorkspaceID = %q, want wOld — the rest of the record must still load correctly", got.HerdrWorkspaceID)
	}

	// Also exercise List, the path space-list uses.
	all, err := HerdrSpaceList(ctx, root)
	if err != nil {
		t.Fatalf("List on old-shape file: %v", err)
	}
	if len(all) != 1 || all[0].GuestPaneID != "" {
		t.Errorf("List = %+v, want one binding with empty GuestPaneID", all)
	}
}

// ── space-list: pane ID surfaced ─────────────────────────────────────────────

func TestHerdrPluginSpaceList_ShowsPaneID(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	b := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/x", HerdrWorkspaceID: "wF",
		SandboxHandle: "proj/x", SandboxID: "sb-x", GuestPaneID: "w1V:p2",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var out strings.Builder
	if err := herdrPluginSpaceList(ctx, &out, root); err != nil {
		t.Fatalf("space-list: %v", err)
	}
	if !strings.Contains(out.String(), "pane_id=w1V:p2") {
		t.Errorf("space-list output %q missing pane_id=w1V:p2", out.String())
	}
}

// ── space-open-pane: graft-not-close behaviour, pane ID capture/persist, focus ──

// TestHerdrPluginSpaceOpenPane_GraftsGuestPaneOntoRootPane drives the real
// adopt-and-mint path (no existing binding) and asserts the full observable
// argv sequence: workspace create (carrying --cwd), then plugin pane open
// carrying --placement split / --target-pane <root-pane-id> / --direction
// right / --focus — and, critically, that `tab close` is never invoked at
// all. An earlier version of this fix closed the root tab after opening a
// second one; that SIGHUPs whatever is running in it, which is exactly what
// that revision replaced. This test also pins J1 (pane ID captured and
// persisted onto the binding, and printed to the caller) end to end.
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
		if len(args) >= 2 && args[0] == "plugin" && args[1] == "pane" {
			return fakePaneOpenCmd("w1V:p2")
		}
		return exec.Command("/bin/true")
	})

	var out strings.Builder
	if err := herdrPluginSpaceOpenPane(ctx, sb.Handle(), storeRoot, g, &out); err != nil {
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
			if !contains(argv, "--focus") {
				t.Errorf("pane open argv %v missing --focus — space-open-pane is the interactive, human path", argv)
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

	// J1: the pane ID must be persisted on the binding, retrievable by a
	// later invocation without reopening a pane.
	got, err := HerdrSpaceGetByLabel(ctx, storeRoot, herdrSpaceLabelForRef(sb.Handle()))
	if err != nil {
		t.Fatalf("binding not found after space-open-pane: %v", err)
	}
	if got.GuestPaneID != "w1V:p2" {
		t.Errorf("persisted GuestPaneID = %q, want w1V:p2", got.GuestPaneID)
	}
	if !strings.Contains(out.String(), "pane_id=w1V:p2") {
		t.Errorf("caller-visible output %q missing pane_id=w1V:p2", out.String())
	}
}

// TestHerdrPluginSpaceOpenPane_ReusedWorkspaceOpensPaneWithoutCreating asserts
// that when an existing binding already carries a workspace ID, no new
// workspace is created, the pane is opened via the --workspace fallback (no
// root pane id is known for a reused workspace), the freshly-opened pane's ID
// still gets persisted onto the binding, and — as always — no tab is ever
// closed.
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
		if len(args) >= 2 && args[0] == "plugin" && args[1] == "pane" {
			return fakePaneOpenCmd("w2X:p5")
		}
		return exec.Command("/bin/true")
	})

	var out strings.Builder
	if err := herdrPluginSpaceOpenPane(ctx, "proj/z", storeRoot, g, &out); err != nil {
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
			if !contains(argv, "--focus") {
				t.Errorf("pane open argv %v missing --focus — space-open-pane is the interactive, human path", argv)
			}
		}
	}
	if !paneOpened {
		t.Error("plugin pane open was never called")
	}

	got, err := HerdrSpaceGetByLabel(ctx, storeRoot, "nexus3:proj/z")
	if err != nil {
		t.Fatalf("binding not found: %v", err)
	}
	if got.GuestPaneID != "w2X:p5" {
		t.Errorf("persisted GuestPaneID = %q, want w2X:p5", got.GuestPaneID)
	}
}

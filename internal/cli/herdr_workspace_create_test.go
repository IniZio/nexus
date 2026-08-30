package cli

// Tests for the stray-tab defect fix and its revisions:
//   - herdrWorkspaceCreate threading a host cwd through to
//     `herdr workspace create --cwd`
//   - splitting the guest pane beside the root pane, then closing that root
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

// ── space-open-pane: split-then-close-root, pane ID capture/persist, focus ──

// TestHerdrPluginSpaceOpenPane_SplitsGuestPaneThenClosesRootPane drives the real
// adopt-and-mint path (no existing binding) and asserts the full observable
// argv sequence: workspace create (carrying --cwd), then plugin pane open
// carrying --placement split / --target-pane <root-pane-id> / --direction
// right / --focus, then pane close <root-pane-id> to leave the workspace
// guest-only. Critically, `tab close` is never called — an earlier revision
// closed the root TAB after opening a second one, which SIGHUPs whatever is
// running in it. This test also pins J1 (pane ID captured and persisted onto
// the binding, and printed to the caller) end to end.
func TestHerdrPluginSpaceOpenPane_SplitsGuestPaneThenClosesRootPane(t *testing.T) {
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
		return exec.Command("/bin/true") // handles pane close and anything else
	})

	var out strings.Builder
	if err := herdrPluginSpaceOpenPane(ctx, sb.Handle(), storeRoot, g, &out); err != nil {
		t.Fatalf("space-open-pane: %v", err)
	}

	var createIdx, paneOpenIdx, paneCloseIdx = -1, -1, -1
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
		case len(argv) >= 4 && argv[1] == "pane" && argv[2] == "close" && argv[3] == "p1":
			paneCloseIdx = i
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
	if paneCloseIdx == -1 {
		t.Fatal("pane close was never called for root pane p1 — workspace has extra host pane")
	}
	if !(createIdx < paneOpenIdx) {
		t.Errorf("expected order create(%d) < pane-open(%d)", createIdx, paneOpenIdx)
	}
	if !(paneOpenIdx < paneCloseIdx) {
		t.Errorf("expected order pane-open(%d) < pane-close(%d) — must open guest pane before closing root", paneOpenIdx, paneCloseIdx)
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

// ── guest-only workspace: pane close discipline ───────────────────────────────

// TestHerdrPluginSpaceOpenPane_ClosesRootPaneAfterGuestPaneOpens is the
// dedicated pin for the guest-only invariant: after the guest pane is
// successfully opened, the root host pane must be closed so the workspace
// contains only the guest shell.
//
// Mutation proof: remove the "pane close" block in herdrPluginSpaceOpenPane,
// run this test, it goes RED with "pane close was never called".
func TestHerdrPluginSpaceOpenPane_ClosesRootPaneAfterGuestPaneOpens(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Project: "proj", Name: "closetest", State: domain.Running,
	}
	g := &fakeAdoptGetter{byRef: map[string]domain.Sandbox{sb.Handle(): sb}}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			return fakeWorkspaceCreateCmd("wC", "rootC")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wC:guestC")
		default:
			return exec.Command("/bin/true") // pane close succeeds
		}
	})

	if err := herdrPluginSpaceOpenPane(ctx, sb.Handle(), storeRoot, g, &strings.Builder{}); err != nil {
		t.Fatalf("space-open-pane: %v", err)
	}

	var paneOpenIdx, paneCloseIdx = -1, -1
	for i, argv := range calls {
		if len(argv) >= 3 && argv[1] == "plugin" && argv[2] == "pane" {
			paneOpenIdx = i
		}
		if len(argv) >= 4 && argv[1] == "pane" && argv[2] == "close" && argv[3] == "rootC" {
			paneCloseIdx = i
		}
	}
	if paneCloseIdx == -1 {
		t.Error("pane close was never called for root pane rootC — workspace has extra host pane")
	}
	if paneOpenIdx != -1 && paneCloseIdx != -1 && !(paneOpenIdx < paneCloseIdx) {
		t.Errorf("pane open(%d) must precede pane close(%d)", paneOpenIdx, paneCloseIdx)
	}
}

// TestHerdrPluginSpaceOpenPane_DoesNotCloseRootPaneIfGuestPaneOpenFails
// guarantees the safety invariant: if the guest pane fails to open, the root
// pane must NOT be closed (closing the last pane destroys the workspace).
func TestHerdrPluginSpaceOpenPane_DoesNotCloseRootPaneIfGuestPaneOpenFails(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Project: "proj", Name: "failopen", State: domain.Running,
	}
	g := &fakeAdoptGetter{byRef: map[string]domain.Sandbox{sb.Handle(): sb}}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			return fakeWorkspaceCreateCmd("wFail", "rootFail")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			// Guest pane open fails.
			return exec.Command("/bin/false")
		default:
			return exec.Command("/bin/true")
		}
	})

	err := herdrPluginSpaceOpenPane(ctx, sb.Handle(), storeRoot, g, &strings.Builder{})
	if err == nil {
		t.Fatal("expected error when guest pane open fails, got nil")
	}

	for _, argv := range calls {
		if len(argv) >= 3 && argv[1] == "pane" && argv[2] == "close" {
			t.Errorf("pane close called (%v) after guest pane open failed — root pane must be left alone", argv)
		}
	}
}

// TestHerdrPluginSpaceOpenPane_CloseRootPaneFailureDoesNotFailSpaceCreate
// pins the failure policy: a pane-close failure is cosmetic. The sandbox is
// fully operational; herdr is a terminal multiplexer and nexus3 is a VM
// manager — a herdr problem must not break a working sandbox.
func TestHerdrPluginSpaceOpenPane_CloseRootPaneFailureDoesNotFailSpaceCreate(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Project: "proj", Name: "closefail", State: domain.Running,
	}
	g := &fakeAdoptGetter{byRef: map[string]domain.Sandbox{sb.Handle(): sb}}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			return fakeWorkspaceCreateCmd("wCF", "rootCF")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wCF:guestCF")
		case len(args) >= 3 && args[0] == "pane" && args[1] == "close":
			// pane close fails — must not propagate as an error.
			return exec.Command("/bin/false")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceOpenPane(ctx, sb.Handle(), storeRoot, g, &out); err != nil {
		t.Fatalf("space-open-pane must succeed even when pane close fails, got: %v", err)
	}

	// Verify pane close was at least attempted.
	paneCloseCalled := false
	for _, argv := range calls {
		if len(argv) >= 4 && argv[1] == "pane" && argv[2] == "close" && argv[3] == "rootCF" {
			paneCloseCalled = true
		}
	}
	if !paneCloseCalled {
		t.Error("pane close was not attempted — the function must try to close the root pane")
	}
}

// ── herdrPluginSpaceCreate: stale-binding fix coverage ───────────────────────
//
// Tests 1–4 pin the fix introduced for the "workspace_not_found" regression:
// herdrPluginSpaceCreate used to reuse a stored binding blindly. If the
// operator closed that workspace in herdr, space-create failed outright
// against a dead binding, stranding the sandbox.
//
// The fix checks the stored workspace still appears in `herdr workspace list`
// before reusing; if absent it mints a fresh one. The predicate
// (herdrSpacePruneWorkspaceExistsFn) fails SAFE: on fetch/parse failure it
// reports every workspace alive, so we reuse rather than minting a duplicate
// during a transient herdr outage.

// fakeSpaceCreateSvc satisfies herdrSpaceCreateSvc for tests that need to
// drive herdrPluginSpaceCreate without a real sandbox service. Start always
// succeeds, returning the configured sandbox.
type fakeSpaceCreateSvc struct {
	sb domain.Sandbox
}

func (f *fakeSpaceCreateSvc) Start(_ context.Context, _ string) (domain.Sandbox, error) {
	return f.sb, nil
}
func (f *fakeSpaceCreateSvc) List(_ context.Context) ([]domain.Sandbox, error) {
	return []domain.Sandbox{f.sb}, nil
}
func (f *fakeSpaceCreateSvc) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return f.sb, nil
}

// TestHerdrPluginSpaceCreate_StaleBindingMints verifies that when a stored
// binding's workspace_id is ABSENT from the live `workspace list` output,
// herdrPluginSpaceCreate does NOT open a pane in the stale workspace, DOES
// call `workspace create`, and the binding is updated to the new workspace ID.
//
// Mutation proof: delete the `if reusable && !herdrSpacePruneWorkspaceExistsFn`
// block so the code reuses blindly → `workspace create` is never called →
// workspaceCreateCalled stays false → test goes RED.
func TestHerdrPluginSpaceCreate_StaleBindingMints(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "stale", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	// Pre-write a binding whose workspace is gone.
	staleBinding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/stale", HerdrWorkspaceID: "wStale",
		SandboxHandle: "proj/stale", SandboxID: sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, storeRoot, staleBinding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var workspaceCreateCalled bool
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			// wStale is NOT in this list → predicate returns false → mint.
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"workspace_id":"wOtherUnrelated"}]}}`)
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			workspaceCreateCalled = true
			return fakeWorkspaceCreateCmd("wNew", "pRoot")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wNew:p1")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	// workspace create must have been called (minted a new workspace).
	if !workspaceCreateCalled {
		t.Error("workspace create was not called; stale binding should have triggered a mint")
	}

	// Binding must be updated to the new workspace, not left pointing at the stale one.
	got, err := HerdrSpaceGetByLabel(ctx, storeRoot, "nexus3:proj/stale")
	if err != nil {
		t.Fatalf("GetByLabel after mint: %v", err)
	}
	if got.HerdrWorkspaceID != "wNew" {
		t.Errorf("binding.HerdrWorkspaceID = %q, want wNew", got.HerdrWorkspaceID)
	}

	// No pane call must reference the stale workspace ID.
	for _, argv := range calls {
		for _, arg := range argv {
			if arg == "wStale" {
				t.Errorf("call argv %v references stale workspace ID wStale", argv)
			}
		}
	}
}

// TestHerdrPluginSpaceCreate_LiveBindingReuses is the regression guard: when
// the stored workspace_id IS present in `workspace list`, workspace create is
// NOT called and the existing workspace is reused.
//
// Mutation proof: force reusable = false unconditionally (always mint) →
// workspaceCreateCalled becomes true → test goes RED, proving the suite can
// distinguish "checks correctly" from "always mints".
func TestHerdrPluginSpaceCreate_LiveBindingReuses(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "live", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	liveBinding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/live", HerdrWorkspaceID: "wLive",
		SandboxHandle: "proj/live", SandboxID: sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, storeRoot, liveBinding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var workspaceCreateCalled bool
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			// wLive IS in the list → predicate returns true → reuse.
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"workspace_id":"wLive"}]}}`)
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			workspaceCreateCalled = true
			return fakeWorkspaceCreateCmd("wUnexpected", "pUnexpected")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wLive:p1")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if workspaceCreateCalled {
		t.Error("workspace create was called; live binding should be reused, not minted")
	}
}

// TestHerdrPluginSpaceCreate_HerdrUnreachableReuses pins the fail-safe
// direction: when `workspace list` returns a non-zero exit code,
// herdrSpacePruneWorkspaceExistsFn reports every workspace alive, so
// herdrPluginSpaceCreate REUSES the existing binding rather than minting a
// duplicate during a transient herdr outage.
func TestHerdrPluginSpaceCreate_HerdrUnreachableReuses(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "outage", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	binding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/outage", HerdrWorkspaceID: "wExisting",
		SandboxHandle: "proj/outage", SandboxID: sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var workspaceCreateCalled bool
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			// Non-zero exit → herdr unreachable → fail-safe: all alive → reuse.
			return exec.Command("/bin/sh", "-c", "exit 1")
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			workspaceCreateCalled = true
			return fakeWorkspaceCreateCmd("wShouldNotCreate", "pRoot")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wExisting:p1")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if workspaceCreateCalled {
		t.Error("workspace create was called during herdr outage; fail-safe must reuse existing binding")
	}
}

// TestHerdrPluginSpaceCreate_MalformedWorkspaceListReuses pins the fail-safe
// for a malformed (but exit-0) workspace list: when `workspace list` returns
// output whose entries all have empty workspace_id (key renamed from
// "workspace_id" to something else), herdrSpacePruneWorkspaceExistsFn sees
// zero non-empty IDs and treats all bindings as alive, so the existing binding
// is REUSED rather than triggering a spurious mint.
func TestHerdrPluginSpaceCreate_MalformedWorkspaceListReuses(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "malformed", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	binding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/malformed", HerdrWorkspaceID: "wExisting",
		SandboxHandle: "proj/malformed", SandboxID: sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var workspaceCreateCalled bool
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			// Field renamed to "id" — all WorkspaceID fields unmarshal as "".
			// Zero non-empty IDs → fail-safe: all alive → reuse.
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"id":"wExisting"},{"id":"wOther"}]}}`)
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			workspaceCreateCalled = true
			return fakeWorkspaceCreateCmd("wShouldNotCreate", "pRoot")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wExisting:p1")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if workspaceCreateCalled {
		t.Error("workspace create was called on malformed list; fail-safe must reuse existing binding")
	}
}

// ── herdrPluginSpaceCreate: pane-close invariants ────────────────────────────
//
// The three tests below mirror the pane-close coverage that exists for
// herdrPluginSpaceOpenPane (SplitsGuestPaneThenClosesRootPane,
// DoesNotCloseRootPaneIfGuestPaneOpenFails,
// CloseRootPaneFailureDoesNotFailSpaceCreate) but drive
// herdrPluginSpaceCreate — the function the operator actually reaches via
// `herdr space-create`, `create-from-file`, and the sandbox launch path.
// Both copies of the close block must be pinned so a future edit cannot fix
// only one.

// TestHerdrPluginSpaceCreate_ClosesRootPaneAfterGuestPaneOpens pins the
// invariant that (a) the root pane IS closed after the guest pane opens,
// (b) pane close comes AFTER the plugin-pane open, and (c) "tab" never
// appears in any argv (closing the tab SIGHUPs running work).
//
// Mutation proof: delete the herdrCloseRootPane(ctx, herdrBin, "space-create",
// rootPaneID) call, confirm RED, restore. Named by SYMBOL, not line range: the
// range this comment used to cite drifted onto the guest-pane open, so anyone
// following it reproduced the wrong mutation and concluded the close was
// covered when it was not.
func TestHerdrPluginSpaceCreate_ClosesRootPaneAfterGuestPaneOpens(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "close-after", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			return fakeWorkspaceCreateCmd("wA", "rootA")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wA:guestA")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	var paneOpenIdx, paneCloseIdx = -1, -1
	for i, argv := range calls {
		if len(argv) >= 3 && argv[1] == "plugin" && argv[2] == "pane" {
			paneOpenIdx = i
		}
		if len(argv) >= 4 && argv[1] == "pane" && argv[2] == "close" && argv[3] == "rootA" {
			paneCloseIdx = i
		}
		for _, a := range argv {
			if a == "tab" {
				t.Errorf("argv %v contains %q — closing a tab SIGHUPs running work; must use pane close", argv, "tab")
			}
		}
	}
	if paneCloseIdx == -1 {
		t.Error("pane close was never called for root pane rootA — workspace has extra host pane")
	}
	if paneOpenIdx != -1 && paneCloseIdx != -1 && !(paneOpenIdx < paneCloseIdx) {
		t.Errorf("pane open(%d) must precede pane close(%d)", paneOpenIdx, paneCloseIdx)
	}
}

// TestHerdrPluginSpaceCreate_DoesNotCloseRootPaneIfGuestPaneOpenFails
// guarantees the safety invariant: if the guest pane fails to open, the root
// pane must NOT be closed (closing the last pane destroys the workspace).
func TestHerdrPluginSpaceCreate_DoesNotCloseRootPaneIfGuestPaneOpenFails(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "guestfail", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			return fakeWorkspaceCreateCmd("wB", "rootB")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return exec.Command("/bin/false")
		default:
			return exec.Command("/bin/true")
		}
	})

	err := herdrPluginSpaceCreate(ctx, sb.Handle(), &strings.Builder{}, svc, storeRoot, false)
	if err == nil {
		t.Fatal("expected error when guest pane open fails, got nil")
	}

	for _, argv := range calls {
		if len(argv) >= 3 && argv[1] == "pane" && argv[2] == "close" {
			t.Errorf("pane close called (%v) after guest pane open failed — root pane must be left alone", argv)
		}
	}
}

// TestHerdrPluginSpaceCreate_CloseRootPaneFailureDoesNotFailSpaceCreate
// pins the failure policy: a pane-close failure is cosmetic. The sandbox is
// fully operational; herdr is a terminal multiplexer and nexus3 is a VM
// manager — a herdr problem must not break a working sandbox.
func TestHerdrPluginSpaceCreate_CloseRootPaneFailureDoesNotFailSpaceCreate(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "closefail2", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "create":
			return fakeWorkspaceCreateCmd("wC2", "rootC2")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			return fakePaneOpenCmd("wC2:guestC2")
		case len(args) >= 3 && args[0] == "pane" && args[1] == "close":
			return exec.Command("/bin/false")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create must succeed even when pane close fails, got: %v", err)
	}

	// Verify pane close was at least attempted.
	paneCloseCalled := false
	for _, argv := range calls {
		if len(argv) >= 4 && argv[1] == "pane" && argv[2] == "close" && argv[3] == "rootC2" {
			paneCloseCalled = true
		}
	}
	if !paneCloseCalled {
		t.Error("pane close was not attempted — the function must try to close the root pane")
	}
}

// ── Bug 2: reuse branch pane-liveness check ──────────────────────────────────
//
// These tests cover the four cases that herdrPaneExistsFn gates:
//
//  1. Recorded pane is LIVE  → no new pane opened; binding unchanged.
//  2. Recorded pane is GONE  → new pane IS opened; binding updated.
//  3. Recorded pane ID EMPTY → new pane opened (pre-existing behaviour).
//  4. Pane-list probe ERRORS → fail-safe: assume dead → new pane opened.
//
// Each test uses fakeHerdrExec to control herdr responses without any live
// herdr process, following the pattern established by
// TestHerdrPluginSpaceCreate_LiveBindingReuses above.

// fakePaneListCmd returns a command whose stdout is a canned
// `herdr pane list --workspace` JSON response containing exactly the listed
// pane IDs.
func fakePaneListCmd(paneIDs ...string) *exec.Cmd {
	panes := ""
	for i, id := range paneIDs {
		if i > 0 {
			panes += ","
		}
		panes += fmt.Sprintf(`{"pane_id":%q}`, id)
	}
	body := fmt.Sprintf(`{"result":{"panes":[%s]}}`, panes)
	return exec.Command("printf", "%s", body)
}

// TestHerdrPluginSpaceCreate_LivePane_NoNewPaneOpened verifies that when the
// stored GuestPaneID is still alive in the workspace, no new pane is opened.
//
// MUTATION PROOF: remove the `if existing.GuestPaneID != "" &&
// herdrPaneExistsFn(...)` guard so the code always opens a new pane →
// paneOpenCalled becomes true → RED: "guest pane was opened despite live pane".
func TestHerdrPluginSpaceCreate_LivePane_NoNewPaneOpened(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "livepane", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	const existingPaneID = "wLP:p1"
	binding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/livepane", HerdrWorkspaceID: "wLP",
		SandboxHandle: "proj/livepane", SandboxID: sb.ID.String(),
		GuestPaneID: existingPaneID,
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	paneOpenCalled := false
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"workspace_id":"wLP"}]}}`)
		case len(args) >= 3 && args[0] == "pane" && args[1] == "list":
			// pane list --workspace wLP: return the existing pane as alive.
			return fakePaneListCmd(existingPaneID)
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			paneOpenCalled = true
			return fakePaneOpenCmd("wLP:p2") // would be a second pane — must not happen
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if paneOpenCalled {
		t.Error("guest pane was opened despite live pane; must reuse existing pane")
	}

	// Binding must still point at the original pane.
	all, _ := herdrSpaceReadAll(storeRoot)
	for _, b := range all {
		if b.HerdrWorkspaceID == "wLP" && b.GuestPaneID != existingPaneID {
			t.Errorf("binding GuestPaneID = %q; want %q", b.GuestPaneID, existingPaneID)
		}
	}
	_ = out.String() // "pane already live" must appear
	if !strings.Contains(out.String(), "pane already live") {
		t.Errorf("output should contain 'pane already live'; got: %q", out.String())
	}
}

// TestHerdrPluginSpaceCreate_GonePane_NewPaneOpened verifies that when the
// stored GuestPaneID is absent from the pane list, a new pane IS opened and
// the binding is updated.
//
// MUTATION PROOF: always return true from herdrPaneExistsFn regardless of
// response → paneOpenCalled stays false → RED: "no new pane opened despite
// pane being gone".
func TestHerdrPluginSpaceCreate_GonePane_NewPaneOpened(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "gonepane", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	const stalePane = "wGP:stale"
	const newPane = "wGP:fresh"
	binding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/gonepane", HerdrWorkspaceID: "wGP",
		SandboxHandle: "proj/gonepane", SandboxID: sb.ID.String(),
		GuestPaneID: stalePane,
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	paneOpenCalled := false
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"workspace_id":"wGP"}]}}`)
		case len(args) >= 3 && args[0] == "pane" && args[1] == "list":
			// stalePane is not in the list — it was closed.
			return fakePaneListCmd("wGP:other")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			paneOpenCalled = true
			return fakePaneOpenCmd(newPane)
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if !paneOpenCalled {
		t.Error("no new pane opened despite recorded pane being gone; must open a replacement")
	}

	// Binding must be updated to the new pane.
	all, _ := herdrSpaceReadAll(storeRoot)
	for _, b := range all {
		if b.HerdrWorkspaceID == "wGP" && b.GuestPaneID != newPane {
			t.Errorf("binding GuestPaneID = %q after update; want %q", b.GuestPaneID, newPane)
		}
	}
}

// TestHerdrPluginSpaceCreate_EmptyGuestPaneID_NewPaneOpened verifies that when
// the stored binding has no GuestPaneID, the pre-existing behaviour is
// unchanged: a new pane is opened unconditionally (pane liveness check is
// skipped for an empty ID).
//
// MUTATION PROOF: change the guard to `existing.GuestPaneID == ""` (inverted)
// → pane liveness is checked even for empty IDs → herdrPaneExistsFn returns
// false → pane open still fires, but now via the liveness-dead path rather
// than the unconditional path → test passes for wrong reasons; alternatively,
// if herdrPaneExistsFn panics on empty input → RED.
func TestHerdrPluginSpaceCreate_EmptyGuestPaneID_NewPaneOpened(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "nopane", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	// Binding with no GuestPaneID (the common case before J1 shipped).
	binding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/nopane", HerdrWorkspaceID: "wNP",
		SandboxHandle: "proj/nopane", SandboxID: sb.ID.String(),
		// GuestPaneID intentionally empty
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	paneOpenCalled := false
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"workspace_id":"wNP"}]}}`)
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			paneOpenCalled = true
			return fakePaneOpenCmd("wNP:p1")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if !paneOpenCalled {
		t.Error("no pane opened for binding with empty GuestPaneID; must open one")
	}
}

// TestHerdrPluginSpaceCreate_PaneListError_FailSafeOpensNewPane verifies the
// fail-safe direction: when the pane-list probe fails (herdr unreachable), the
// code opens a new pane rather than assuming the old one is alive (which would
// silently dispatch the agent nowhere).
//
// MUTATION PROOF: change herdrPaneExistsFn to return true on any error →
// paneOpenCalled stays false → RED: "no new pane opened despite pane-list
// error; fail-safe must open a new pane".
func TestHerdrPluginSpaceCreate_PaneListError_FailSafeOpensNewPane(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")
	storeRoot := t.TempDir()
	ctx := context.Background()

	sb := domain.Sandbox{ID: domain.NewSandboxID(), Project: "proj", Name: "paneerr", State: domain.Running}
	svc := &fakeSpaceCreateSvc{sb: sb}

	const stalePane = "wPE:old"
	binding := HerdrSpaceBinding{
		SpaceLabel: "nexus3:proj/paneerr", HerdrWorkspaceID: "wPE",
		SandboxHandle: "proj/paneerr", SandboxID: sb.ID.String(),
		GuestPaneID: stalePane,
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	paneOpenCalled := false
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd {
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			return exec.Command("printf", "%s",
				`{"result":{"workspaces":[{"workspace_id":"wPE"}]}}`)
		case len(args) >= 3 && args[0] == "pane" && args[1] == "list":
			// Simulate herdr returning non-zero (unreachable / internal error).
			return exec.Command("/bin/sh", "-c", "exit 1")
		case len(args) >= 2 && args[0] == "plugin" && args[1] == "pane":
			paneOpenCalled = true
			return fakePaneOpenCmd("wPE:new")
		default:
			return exec.Command("/bin/true")
		}
	})

	var out strings.Builder
	if err := herdrPluginSpaceCreate(ctx, sb.Handle(), &out, svc, storeRoot, false); err != nil {
		t.Fatalf("space-create: %v", err)
	}

	if !paneOpenCalled {
		t.Error("no new pane opened despite pane-list error; fail-safe must open a new pane rather than silently adopting a potentially dead one")
	}
}

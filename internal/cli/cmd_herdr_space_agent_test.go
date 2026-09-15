package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"slices"
)

// TestSpaceAgentSubcommand_MissingArgs verifies usage error when space-agent has no/few args.
func TestSpaceAgentSubcommand_MissingArgs(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	err := runHerdrPlugin(context.Background(), []string{"space-agent"}, out)
	if err == nil {
		t.Fatal("space-agent with no args: expected error, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}

	err = runHerdrPlugin(context.Background(), []string{"space-agent", "myproj/mybox"}, out)
	if err == nil {
		t.Fatal("space-agent with ref only: expected error, got nil")
	}
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// TestSpaceAgentFromFileSubcommand_Routes verifies space-agent-from-file routing (not "unknown subcommand").
func TestSpaceAgentFromFileSubcommand_Routes(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"space-agent-from-file"}, out)
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("space-agent-from-file not wired in dispatch switch: %v", err)
	}
}

// sandboxGetterWithMount satisfies sandboxGetter with a live mount.
type sandboxGetterWithMount struct {
	mountGuestPath string
}

func (g *sandboxGetterWithMount) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{
		LiveMounts: []domain.LiveMount{{GuestPath: g.mountGuestPath}},
	}, nil
}

// sandboxGetterNoMount satisfies sandboxGetter with no mounts.
type sandboxGetterNoMount struct{}

func (g *sandboxGetterNoMount) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}

// sandboxGetterError satisfies sandboxGetter by returning an error.
type sandboxGetterError struct{ err error }

func (g *sandboxGetterError) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, g.err
}

// TestSpaceAgent_RefusesWhenNoMount verifies early-fail guard on missing mount.
// Design: both refusal types (no mount vs. not found) must produce different advice.
func TestSpaceAgent_RefusesWhenNoMount(t *testing.T) {
	execCalled := false
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "false")
	}
	defer func() { herdrExecCommandContext = old }()
	cases := []struct {
		name    string
		getter  sandboxGetter
		wantSub string
		notSub  string
	}{
		{
			name:    "sandbox exists but has no mounted source",
			getter:  &sandboxGetterNoMount{},
			wantSub: "--mount",
			notSub:  "no such sandbox",
		},
		{
			name:    "sandbox does not resolve at all",
			getter:  &sandboxGetterError{err: errors.New("not found")},
			wantSub: "no such sandbox",
			notSub:  "--mount",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execCalled = false
			dir, err := herdrSpaceAgentProjectDir(context.Background(), "proj/box", tc.getter)
			if err == nil {
				t.Fatalf("expected a refusal, got project dir %q", dir)
			}
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("expected *UsageError, got %T: %v", err, err)
			}
			if !strings.Contains(ue.Msg, tc.wantSub) {
				t.Errorf("message must contain %q; got: %q", tc.wantSub, ue.Msg)
			}
			if strings.Contains(ue.Msg, tc.notSub) {
				t.Errorf("message must NOT contain %q (that is the other refusal's advice); got: %q", tc.notSub, ue.Msg)
			}
			if execCalled {
				t.Error("herdr must not be invoked when the guard refuses")
			}
		})
	}

	if execCalled {
		t.Error("herdr must not be invoked before the mount guard fires")
	}
}

// TestHerdrPaneRun_ArgvShape verifies herdrPaneRun builds correct argv.
func TestHerdrPaneRun_ArgvShape(t *testing.T) {
	var capturedName string
	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	const herdrBin = "/usr/bin/herdr"
	const paneID = "w1V:p2"
	const text = "claude"

	if err := herdrPaneRun(context.Background(), herdrBin, paneID, text); err != nil {
		t.Fatalf("herdrPaneRun: %v", err)
	}

	if capturedName != herdrBin {
		t.Errorf("binary: got %q, want %q", capturedName, herdrBin)
	}
	wantArgs := []string{"pane", "run", paneID, text}
	if len(capturedArgs) != len(wantArgs) {
		t.Fatalf("args len: got %d (%v), want %d (%v)", len(capturedArgs), capturedArgs, len(wantArgs), wantArgs)
	}
	for i, want := range wantArgs {
		if capturedArgs[i] != want {
			t.Errorf("args[%d]: got %q, want %q", i, capturedArgs[i], want)
		}
	}
}

// TestHerdrPaneWaitOutput_ArgvShape verifies herdrPaneWaitOutput argv with --match and --timeout.
func TestHerdrPaneWaitOutput_ArgvShape(t *testing.T) {
	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	const herdrBin = "/usr/bin/herdr"
	const paneID = "w1V:p2"
	const match = "? for shortcuts"
	const timeout = 90_000

	if err := herdrPaneWaitOutput(context.Background(), herdrBin, paneID, match, timeout); err != nil {
		t.Fatalf("herdrPaneWaitOutput: %v", err)
	}

	wantArgs := []string{"pane", "wait-output", paneID, "--match", match, "--timeout", "90000"}
	if len(capturedArgs) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", capturedArgs, wantArgs)
	}
	for i, want := range wantArgs {
		if capturedArgs[i] != want {
			t.Errorf("args[%d]: got %q, want %q", i, capturedArgs[i], want)
		}
	}
}

// TestHerdrPaneReportAgent_ArgvShape verifies herdrPaneReportAgent argv with required flags.
func TestHerdrPaneReportAgent_ArgvShape(t *testing.T) {
	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	const herdrBin = "/usr/bin/herdr"
	const paneID = "w1V:p2"
	const source = "myproj/mybox"

	if err := herdrPaneReportAgent(context.Background(), herdrBin, paneID, source); err != nil {
		t.Fatalf("herdrPaneReportAgent: %v", err)
	}
	if len(capturedArgs) < 2 || capturedArgs[0] != "pane" || capturedArgs[1] != "report-agent" {
		t.Fatalf("expected [pane report-agent ...], got %v", capturedArgs)
	}
	if capturedArgs[2] != paneID {
		t.Errorf("args[2] (pane ID): got %q, want %q", capturedArgs[2], paneID)
	}
	containsFlag := func(flag, val string) bool {
		for i, a := range capturedArgs {
			if a == flag && i+1 < len(capturedArgs) && capturedArgs[i+1] == val {
				return true
			}
		}
		return false
	}
	if !containsFlag("--source", source) {
		t.Errorf("--source %q not found in args: %v", source, capturedArgs)
	}
	if !containsFlag("--state", "working") {
		t.Errorf("--state working not found in args: %v", capturedArgs)
	}
	if idx := indexOf(capturedArgs, "--agent"); idx < 0 || idx+1 >= len(capturedArgs) {
		t.Errorf("--agent flag missing in args: %v", capturedArgs)
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// TestClaudeReadyMatch_IsModeSpecific pins readiness token to permission mode.
/** MUTATION-PIN: prior impl used one token for both modes; it silently failed. */
func TestClaudeReadyMatch_IsModeSpecific(t *testing.T) {
	autonomous := claudeReadyMatch(true)
	normal := claudeReadyMatch(false)

	if autonomous == normal {
		t.Fatalf("the two permission modes print different footers, so they cannot share a "+
			"readiness token; both returned %q", autonomous)
	}

	const (
		normalFooter     = " ⏸ manual mode on · ? for shortcuts · ← for agents"
		autonomousFooter = "⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	)
	if !strings.Contains(normalFooter, normal) {
		t.Errorf("default-mode token %q does not appear in the default-mode footer %q", normal, normalFooter)
	}
	if !strings.Contains(autonomousFooter, autonomous) {
		t.Errorf("autonomous token %q does not appear in the autonomous footer %q", autonomous, autonomousFooter)
	}
	if strings.Contains(autonomousFooter, normal) {
		t.Errorf("default-mode token %q also matches the autonomous footer; the modes are not distinguished", normal)
	}
	if strings.Contains(normalFooter, autonomous) {
		t.Errorf("autonomous token %q also matches the default-mode footer; the modes are not distinguished", autonomous)
	}

	wizards := []string{
		" ❯ 2. Dark mode ✔",
		" ❯ 1. Claude account with subscription · Pro, Max,",
		" ❯ 1. Yes, I trust this folder",
		" ❯ 1. No, exit",
	}
	for _, wiz := range wizards {
		for _, tok := range []string{normal, autonomous} {
			if strings.Contains(wiz, tok) {
				t.Errorf("token %q matches wizard line %q; it would report ready mid-dialog", tok, wiz)
			}
		}
	}
}

// TestHerdrPaneSubmitToAgent_SendsTextThenEnterSeparately pins two-call submit.
/** Design: `herdr pane run` fails against TUI; text needs separate Enter.
  PACING: observed live, collapsing to one call silently breaks the agent. */
func TestHerdrPaneSubmitToAgent_SendsTextThenEnterSeparately(t *testing.T) {
	var got [][]string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, args)
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	if err := herdrPaneSubmitToAgent(context.Background(), "herdr", "w1:p2", "do the thing"); err != nil {
		t.Fatalf("herdrPaneSubmitToAgent: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected exactly 2 herdr calls (send-text, then send-keys Enter); got %d: %v", len(got), got)
	}
	wantText := []string{"pane", "send-text", "w1:p2", "do the thing"}
	if !slices.Equal(got[0], wantText) {
		t.Errorf("first call must place the text without submitting it\n got: %v\nwant: %v", got[0], wantText)
	}
	wantEnter := []string{"pane", "send-keys", "w1:p2", "Enter"}
	if !slices.Equal(got[1], wantEnter) {
		t.Errorf("second call must submit with a separate Enter\n got: %v\nwant: %v", got[1], wantEnter)
	}
	for _, call := range got {
		if len(call) > 1 && call[1] == "run" {
			t.Errorf("must not use `pane run` here: it sends text and Enter together, "+
				"which claude's TUI does not accept as a submit; got %v", call)
		}
	}
}

// TestGuestAgentLaunchCommand_DoesNotDependOnTheShellFunction pins self-contained launch.
/** Design: --permission-mode auto and IS_SANDBOX=1 must be explicit.
  PACING: race on /etc/profile.d; observed live: bareword "claude" silently fails. */
func TestGuestAgentLaunchCommand_DoesNotDependOnTheShellFunction(t *testing.T) {
	const permFlag = "--permission-mode auto"
	const oldBypassFlag = "--dangerously-skip-permissions"

	autonomous := guestAgentLaunchCommand(true)
	if !strings.Contains(autonomous, permFlag) {
		t.Errorf("autonomous launch must pass %s explicitly; got %q", permFlag, autonomous)
	}
	if strings.Contains(autonomous, oldBypassFlag) {
		t.Errorf("autonomous launch must not use the retired %s flag; got %q", oldBypassFlag, autonomous)
	}
	if !strings.Contains(autonomous, "IS_SANDBOX=1") {
		t.Errorf("autonomous launch must set IS_SANDBOX=1: claude refuses %s as root without it; got %q",
			permFlag, autonomous)
	}

	normal := guestAgentLaunchCommand(false)
	if strings.Contains(normal, permFlag) {
		t.Errorf("non-autonomous launch must not pass %s; got %q", permFlag, normal)
	}
	if strings.Contains(normal, oldBypassFlag) {
		t.Errorf("non-autonomous launch must not pass %s; got %q", oldBypassFlag, normal)
	}
	if !strings.HasPrefix(normal, "command ") {
		t.Errorf("non-autonomous launch must use `command claude` to bypass any shell function; got %q", normal)
	}

	if claudeReadyMatch(true) == claudeReadyMatch(false) {
		t.Error("readiness tokens for the two modes collapsed; see claudeReadyMatch")
	}
}

// TestGuestCursorLaunchCommand_ForceFlagAndNoRootEscape pins cursor launch command flags.
/** Design: --force is skip-permissions equivalent; no IS_SANDBOX needed for cursor. */
func TestGuestCursorLaunchCommand_ForceFlagAndNoRootEscape(t *testing.T) {
	autonomous := guestCursorLaunchCommand(true)
	if !strings.Contains(autonomous, "--force") {
		t.Errorf("autonomous cursor launch must pass --force; got %q", autonomous)
	}
	if strings.Contains(autonomous, "IS_SANDBOX") {
		t.Errorf("cursor-agent needs no IS_SANDBOX escape (it does not refuse root); got %q", autonomous)
	}

	normal := guestCursorLaunchCommand(false)
	if strings.Contains(normal, "--force") {
		t.Errorf("non-autonomous cursor launch must not pass --force; got %q", normal)
	}
	if strings.HasPrefix(normal, "command ") {
		t.Errorf("cursor has no shell function to bypass with `command `; got %q", normal)
	}
}

// TestResolveAgentLaunchDescriptor_DispatchesByName is mutation guard for agent dispatch.
/** Design: AgentName must select matching launch command, not always claude's. */
func TestResolveAgentLaunchDescriptor_DispatchesByName(t *testing.T) {
	cursor := resolveAgentLaunchDescriptor(cred.CursorAgentProfileName)
	if got, want := cursor.command(true), guestCursorLaunchCommand(true); got != want {
		t.Errorf("cursor descriptor command(true) = %q, want %q (guestAgentLaunchCommand leaked through)", got, want)
	}
	if got, want := cursor.readyMatch(true), cursorReadyMatch(true); got != want {
		t.Errorf("cursor descriptor readyMatch(true) = %q, want %q", got, want)
	}
	if cursor.command(true) == guestAgentLaunchCommand(true) {
		t.Error("cursor descriptor resolved to claude's launch command — dispatch is not agent-specific")
	}

	claude := resolveAgentLaunchDescriptor(cred.ClaudeCodeProfileName)
	if got, want := claude.command(true), guestAgentLaunchCommand(true); got != want {
		t.Errorf("claude descriptor command(true) = %q, want %q", got, want)
	}

	// Empty (plain sandbox) and unrecognised names fall back to claude's
	// descriptor, matching cred.DefaultProfileName / pre-registry behaviour.
	for _, name := range []string{"", "some-future-agent-not-yet-registered"} {
		fallback := resolveAgentLaunchDescriptor(name)
		if got, want := fallback.command(true), guestAgentLaunchCommand(true); got != want {
			t.Errorf("resolveAgentLaunchDescriptor(%q) command(true) = %q, want claude's %q", name, got, want)
		}
	}
}

// ── J2: space-agent --no-focus propagates through herdrOpenGuestShellPane ────

// TestSpaceAgent_PaneOpenFocusArgv_WithFocus asserts focus=true produces --focus.
func TestSpaceAgent_PaneOpenFocusArgv_WithFocus(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return fakePaneOpenCmd("w1:p1") })

	if _, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/a", "wW", "pR", true); err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("herdrOpenGuestShellPane made no herdr calls")
	}
	argv := calls[0]
	if !contains(argv, "--focus") {
		t.Errorf("argv %v missing --focus when focus=true", argv)
	}
	if contains(argv, "--no-focus") {
		t.Errorf("argv %v contains --no-focus when focus=true — mutually exclusive with --focus", argv)
	}
}

// TestSpaceAgent_PaneOpenFocusArgv_WithNoFocus asserts focus=false produces --no-focus.
/** AC-5: concurrent space-agent runs must not steal focus. */
func TestSpaceAgent_PaneOpenFocusArgv_WithNoFocus(t *testing.T) {
	var calls [][]string
	fakeHerdrExec(t, &calls, func(args []string) *exec.Cmd { return fakePaneOpenCmd("w1:p2") })

	if _, err := herdrOpenGuestShellPane(context.Background(), "/fake/herdr", "proj/b", "wW", "pR", false); err != nil {
		t.Fatalf("herdrOpenGuestShellPane: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("herdrOpenGuestShellPane made no herdr calls")
	}
	argv := calls[0]
	if contains(argv, "--focus") {
		t.Errorf("argv %v contains --focus when focus=false — concurrent runs must not steal focus", argv)
	}
	if !contains(argv, "--no-focus") {
		t.Errorf("argv %v missing --no-focus when focus=false — must pass explicitly, not just omit --focus", argv)
	}
}

// TestCursorReadyMatch_AgainstLiveCapturedPaneOutput exercises readyMatch against live pane.
/** PACING: captured 2026-09-04 from cursor-agent v2026.09.02-c22c1a3.
  Design: static "shift+tab" guess absent in real output; replaced with live token. */
func TestCursorReadyMatch_AgainstLiveCapturedPaneOutput(t *testing.T) {
	const readyPane = `  Cursor Agent
  v2026.09.02-c22c1a3
  Tip: Type ? in the prompt bar to show in-app hints.

  → Plan, search, build anything

  Cursor Grok 4.5 High Fast
  ~/magic/nexus3/.claude/worktrees/agent-a81fcacc4e0838ade · nexus3/cursor-s6-readymatch`

	const stillStartingPane = `  Cursor Agent
  v2026.09.02-c22c1a3`

	const unauthenticatedPane = `  Cursor Agent
  v2026.09.02-c22c1a3
  Not logged in. Run: cursor-agent login`

	tok := cursorReadyMatch(false)
	if tok == "" {
		t.Fatal("cursorReadyMatch returned empty string — would wait forever")
	}

	// Autonomous mode must return the same token: cursor's ready state does not
	// change based on --force/--yolo.
	tokAuto := cursorReadyMatch(true)
	if tok != tokAuto {
		t.Errorf("cursorReadyMatch is mode-invariant for cursor, but got different tokens: normal=%q autonomous=%q", tok, tokAuto)
	}

	if !strings.Contains(readyPane, tok) {
		t.Errorf("readyMatch token %q not found in the live-captured ready pane:\n%s", tok, readyPane)
	}

	if strings.Contains(stillStartingPane, tok) {
		t.Errorf("readyMatch token %q fires on a still-starting pane — would report ready too early:\n%s", tok, stillStartingPane)
	}

	if strings.Contains(unauthenticatedPane, tok) {
		t.Errorf("readyMatch token %q fires on an unauthenticated pane — would report ready without a session:\n%s", tok, unauthenticatedPane)
	}

	const staleGuess = "shift+tab to cycle"
	if tok == staleGuess {
		t.Errorf("cursorReadyMatch still returns the stale static-analysis guess %q — "+
			"this string does not appear in real cursor-agent TUI output; update it to match the live capture", staleGuess)
	}
	if strings.Contains(readyPane, staleGuess) {
		t.Errorf("stale guess %q unexpectedly appears in the live-captured pane — re-evaluate", staleGuess)
	}
}

// TestSpaceAgentSubcommand_NoFocusFlagParsed verifies --no-focus flag is parsed.
func TestSpaceAgentSubcommand_NoFocusFlagParsed(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"space-agent", "--no-focus", "proj/x", "do something"}, out)
	if err == nil {
		t.Fatal("expected an error (no real sandbox), got nil")
	}
	if ue, ok := err.(*UsageError); ok && strings.Contains(ue.Msg, "--no-focus") {
		t.Errorf("--no-focus was not parsed; got usage error: %v", ue)
	}
}

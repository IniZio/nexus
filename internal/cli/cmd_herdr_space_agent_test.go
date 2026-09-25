package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	herdragent "github.com/IniZio/nexus/internal/herdragent"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
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

// TestClaudeReadyMatch_NeverMatchesAWizard pins that the readiness token is
// not the prompt glyph: ❯ is also the selector glyph in every first-run wizard.
func TestClaudeReadyMatch_NeverMatchesAWizard(t *testing.T) {
	const autonomousFooter = "⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	tok := claudeReadyMatch(true)
	if !strings.Contains(autonomousFooter, tok) {
		t.Errorf("token %q does not appear in the auto-mode footer %q", tok, autonomousFooter)
	}

	wizards := []string{
		" ❯ 2. Dark mode ✔",
		" ❯ 1. Claude account with subscription · Pro, Max,",
		" ❯ 1. Yes, I trust this folder",
		" ❯ 1. No, exit",
	}
	for _, wiz := range wizards {
		if strings.Contains(wiz, tok) {
			t.Errorf("token %q matches wizard line %q; it would report ready mid-dialog", tok, wiz)
		}
	}
}

// TestHerdrPaneSubmitToAgent_SendsTextThenEnterSeparately pins two-call submit.
// Design: `herdr pane run` fails against TUI; text needs separate Enter.
// PACING: observed live, collapsing to one call silently breaks the agent.
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
// Design: --permission-mode auto and IS_SANDBOX=1 must be explicit, in BOTH
// branches — D-2 says the guest always runs auto, and D-1's live ~/.claude mount
// means a bare `command claude` starts in whatever mode the host settings.json
// says, which the readiness wait cannot predict.
// PACING: race on /etc/profile.d; observed live: bareword "claude" silently fails.
func TestGuestAgentLaunchCommand_DoesNotDependOnTheShellFunction(t *testing.T) {
	const permFlag = "--permission-mode auto"
	const oldBypassFlag = "--dangerously-skip-permissions"

	for _, autonomous := range []bool{true, false} {
		cmd := guestAgentLaunchCommand(autonomous)
		if !strings.Contains(cmd, permFlag) {
			t.Errorf("guestAgentLaunchCommand(%v) must pass %s explicitly; got %q", autonomous, permFlag, cmd)
		}
		if strings.Contains(cmd, oldBypassFlag) {
			t.Errorf("guestAgentLaunchCommand(%v) must not use the retired %s flag; got %q", autonomous, oldBypassFlag, cmd)
		}
		if !strings.Contains(cmd, "IS_SANDBOX=1") {
			t.Errorf("guestAgentLaunchCommand(%v) must set IS_SANDBOX=1: claude refuses %s as root without it; got %q",
				autonomous, permFlag, cmd)
		}
		if strings.HasPrefix(cmd, "command ") {
			t.Errorf("guestAgentLaunchCommand(%v) uses `command claude`, which starts in the host settings.json mode; got %q", autonomous, cmd)
		}
	}
}

// TestGuestCursorLaunchCommand_ForceFlagAndNoRootEscape pins cursor launch command flags.
// Design: --force is skip-permissions equivalent; no IS_SANDBOX needed for cursor.
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
// Design: AgentName must select matching launch command, not always claude's.
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
// AC-5: concurrent space-agent runs must not steal focus.
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
// PACING: captured 2026-09-04 from cursor-agent v2026.09.02-c22c1a3.
// Design: static "shift+tab" guess absent in real output; replaced with live token.
func TestCursorReadyMatch_AgainstLiveCapturedPaneOutput(t *testing.T) {
	const readyPane = `  Cursor Agent
  v2026.09.02-c22c1a3
  Tip: Type ? in the prompt bar to show in-app hints.

  → Plan, search, build anything

  Cursor Grok 4.5 High Fast
  ~/magic/nexus/.claude/worktrees/agent-a81fcacc4e0838ade · nexus/cursor-s6-readymatch`

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

// fastSleepOpt returns a zero-delay WithSleep option for tests.
func fastSleepOpt() herdragent.Option {
	return herdragent.WithSleep(func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	})
}

// idleClientSeq0 returns a Client always reporting idle seq=0 (Ready never succeeds).
func idleClientSeq0() *herdragent.Client {
	return herdragent.New(
		func(_ context.Context, _ ...string) (string, error) {
			return `{"result":{"agent":{"agent_status":"idle","state_change_seq":0}}}`, nil
		},
		herdragent.WithSettle(0),
		fastSleepOpt(),
	)
}

// unknownClient returns a Client where the runner always fails (herdr unavailable).
func unknownClient() *herdragent.Client {
	return herdragent.New(
		func(_ context.Context, _ ...string) (string, error) {
			return "", errors.New("herdr unavailable")
		},
		herdragent.WithSettle(0),
		fastSleepOpt(),
	)
}

// TestSpaceAgent_WaitsForHerdrReadyBeforeBrief: Observe baseline taken before launch command.
func TestSpaceAgent_WaitsForHerdrReadyBeforeBrief(t *testing.T) {
	var callOrder []string

	oldExec := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) >= 2 && args[0] == "pane" && args[1] == "run" {
			callOrder = append(callOrder, "pane-run")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { herdrExecCommandContext = oldExec })

	var getCount int
	wrappedRunner := func(ctx context.Context, argv ...string) (string, error) {
		if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "get" {
			callOrder = append(callOrder, "agent-get")
			getCount++
		}
		if getCount <= 1 {
			return `{"result":{"agent":{"agent_status":"idle","state_change_seq":0}}}`, nil
		}
		return `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`, nil
	}
	wrappedClient := herdragent.New(wrappedRunner, herdragent.WithSettle(0), fastSleepOpt())

	var w bytes.Buffer
	if err := herdrWaitAgentReady(context.Background(), "herdr", "w1:p1",
		"claude --auto", "auto mode on", wrappedClient, 200, &w); err != nil {
		t.Fatalf("herdrWaitAgentReady: %v", err)
	}

	firstGetIdx, runIdx := -1, -1
	for i, c := range callOrder {
		if c == "agent-get" && firstGetIdx < 0 {
			firstGetIdx = i
		}
		if c == "pane-run" && runIdx < 0 {
			runIdx = i
		}
	}
	if firstGetIdx < 0 {
		t.Error("agent-get never called — baseline Observe was never taken")
	}
	if runIdx < 0 {
		t.Error("pane-run never called — launch command was never sent")
	}
	if firstGetIdx >= 0 && runIdx >= 0 && firstGetIdx >= runIdx {
		t.Errorf("baseline (agent-get at %d) came after or same as launch (pane-run at %d) — "+
			"idle pane before launch could be mistaken for agent ready",
			firstGetIdx, runIdx)
	}
}

// TestSpaceAgent_IdleBeforeLaunchIsNotReady: idle seq=0 both sides → readyMatch fallback fires.
func TestSpaceAgent_IdleBeforeLaunchIsNotReady(t *testing.T) {
	client := idleClientSeq0()

	var execCalls [][]string
	oldExec := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		execCalls = append(execCalls, args)
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { herdrExecCommandContext = oldExec })

	var w bytes.Buffer
	if err := herdrWaitAgentReady(context.Background(), "herdr", "w1:p1",
		"claude --auto", "auto mode on", client, 50, &w); err != nil {
		t.Fatalf("expected readyMatch fallback to succeed; got: %v", err)
	}

	waitOutputCalled := false
	for _, args := range execCalls {
		if len(args) >= 2 && args[0] == "pane" && args[1] == "wait-output" {
			waitOutputCalled = true
		}
	}
	if !waitOutputCalled {
		t.Error("idle pre-launch state did not trigger readyMatch fallback (herdrPaneWaitOutput not called)")
	}
}

// TestSpaceAgent_AgentUnknown_FallsBackToReadyMatch: Unknown herdr runner → readyMatch fallback.
func TestSpaceAgent_AgentUnknown_FallsBackToReadyMatch(t *testing.T) {
	client := unknownClient()

	var execCalls [][]string
	oldExec := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		execCalls = append(execCalls, args)
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { herdrExecCommandContext = oldExec })

	var w bytes.Buffer
	if err := herdrWaitAgentReady(context.Background(), "herdr", "w1:p1",
		"claude --auto", "auto mode on", client, 50, &w); err != nil {
		t.Fatalf("unexpected error on Unknown state: %v", err)
	}

	waitOutputCalled := false
	for _, args := range execCalls {
		if len(args) >= 2 && args[0] == "pane" && args[1] == "wait-output" {
			waitOutputCalled = true
		}
	}
	if !waitOutputCalled {
		t.Error("Unknown herdr state did not trigger readyMatch fallback (herdrPaneWaitOutput not called)")
	}
}

// TestSpaceAgent_AcceptedBrief_NoStranded: classifier SUBMITTED → herdrConfirmDelivery returns nil.
func TestSpaceAgent_AcceptedBrief_NoStranded(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{
		{paneSubmitted, true},
		{paneSubmittedTick, true},
	}, &calls)

	client := idleClientSeq0()

	var w bytes.Buffer
	preDelivery := herdragent.State{Status: herdragent.StatusIdle, Seq: 0}
	if err := herdrConfirmDelivery(context.Background(), "herdr", "w7P:p2",
		"brief text", client, preDelivery, &w); err != nil {
		t.Fatalf("accepted brief was not confirmed: %v", err)
	}
	if strings.Contains(w.String(), "STRANDED") {
		t.Errorf("accepted brief log contains STRANDED: %s", w.String())
	}
}

// TestSpaceAgent_NeverWorking_FailsNotDelivered: idle herdr + UNKNOWN classifier → error naming the pane.
func TestSpaceAgent_NeverWorking_FailsNotDelivered(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{{"", false}}, &calls)

	client := idleClientSeq0()

	var w bytes.Buffer
	preDelivery := herdragent.State{Status: herdragent.StatusIdle, Seq: 0}
	err := herdrConfirmDelivery(context.Background(), "herdr", "w7P:p2",
		"brief text", client, preDelivery, &w)
	if err == nil {
		t.Fatal("idle agent should not confirm delivery — expected non-zero exit")
	}
	if !strings.Contains(err.Error(), "w7P:p2") {
		t.Errorf("error must name the pane for operator inspection; got: %v", err)
	}
}

// TestSpaceAgent_DoesNotForceReportWorking: herdrPluginSpaceAgent must not issue report-agent --state working.
func TestSpaceAgent_DoesNotForceReportWorking(t *testing.T) {
	src, err := os.ReadFile("cmd_herdr_plugin.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := spaceAgentFuncBody(t, string(src))
	if strings.Contains(body, "report-agent") {
		t.Error("herdrPluginSpaceAgent references report-agent — forced --state working " +
			"takes lifecycle authority from herdr native detection; remove the call")
	}
}

// TestSpaceAgent_ReleasesReportedAgent: herdrPaneReleaseAgent called; no report-agent in body.
func TestSpaceAgent_ReleasesReportedAgent(t *testing.T) {
	src, err := os.ReadFile("cmd_herdr_plugin.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := spaceAgentFuncBody(t, string(src))
	if !strings.Contains(body, "herdrPaneReleaseAgent(") {
		t.Error("herdrPluginSpaceAgent does not call herdrPaneReleaseAgent — " +
			"stale lifecycle claims from older binaries block herdr native detection")
	}
	if strings.Contains(body, "report-agent") {
		t.Error("herdrPluginSpaceAgent references report-agent — forced report-agent " +
			"masks native herdr detection; remove the call")
	}
}

// TestConfirmDelivery_HerdrWorking_NoExtraEnter: herdr working → 1 Enter only, classifier skipped.
func TestConfirmDelivery_HerdrWorking_NoExtraEnter(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{{paneSubmitted, true}}, &calls)

	client := herdragent.New(
		func(_ context.Context, _ ...string) (string, error) {
			return `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`, nil
		},
		herdragent.WithSettle(0),
		fastSleepOpt(),
	)

	var w bytes.Buffer
	pre := herdragent.State{Status: herdragent.StatusIdle, Seq: 0}
	if err := herdrConfirmDelivery(context.Background(), "herdr", "w1:p1",
		"brief", client, pre, &w); err != nil {
		t.Fatalf("working herdr state should confirm delivery; got: %v", err)
	}

	var enterCount int
	for _, args := range argv {
		if len(args) >= 2 && args[1] == "send-keys" && args[len(args)-1] == "Enter" {
			enterCount++
		}
	}
	if enterCount != 1 {
		t.Errorf("expected exactly 1 Enter (initial submit), got %d; extra Enters must not be sent after herdr confirms working", enterCount)
	}
	if strings.Contains(w.String(), "classifier") || strings.Contains(w.String(), "STRANDED") {
		t.Errorf("screen classifier must not be consulted when herdr state is working; log: %s", w.String())
	}
}

// TestConfirmDelivery_HerdrUnknown_UsesScreenClassifier: Unknown runner → screen classifier confirms.
func TestConfirmDelivery_HerdrUnknown_UsesScreenClassifier(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{
		{paneSubmitted, true},
		{paneSubmittedTick, true},
	}, &calls)

	var w bytes.Buffer
	pre := herdragent.State{Status: herdragent.StatusUnknown, Seq: 0}
	if err := herdrConfirmDelivery(context.Background(), "herdr", "w1:p1",
		"brief", unknownClient(), pre, &w); err != nil {
		t.Fatalf("screen classifier should confirm delivery when herdr Unknown; got: %v", err)
	}
}

// TestConfirmDelivery_NoEnterAfterWorking: herdr working before retry → no 2nd Enter sent.
func TestConfirmDelivery_NoEnterAfterWorking(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{{"", false}}, &calls)

	observeCount := 0
	client := herdragent.New(
		func(_ context.Context, argv ...string) (string, error) {
			if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "get" {
				observeCount++
			}
			if observeCount <= 1 {
				return `{"result":{"agent":{"agent_status":"idle","state_change_seq":0}}}`, nil
			}
			return `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`, nil
		},
		herdragent.WithSettle(0),
		fastSleepOpt(),
	)

	var w bytes.Buffer
	pre := herdragent.State{Status: herdragent.StatusIdle, Seq: 0}
	if err := herdrConfirmDelivery(context.Background(), "herdr", "w1:p1",
		"brief", client, pre, &w); err != nil {
		t.Fatalf("herdr working before retry should confirm delivery; got: %v", err)
	}

	var enterCount int
	for _, args := range argv {
		if len(args) >= 2 && args[1] == "send-keys" && args[len(args)-1] == "Enter" {
			enterCount++
		}
	}
	if enterCount != 1 {
		t.Errorf("expected exactly 1 Enter (initial submit only), got %d; herdr working must block retry Enter", enterCount)
	}
}

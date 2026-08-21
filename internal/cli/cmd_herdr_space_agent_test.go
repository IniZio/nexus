package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"slices"
)

// TestSpaceAgentSubcommand_MissingArgs verifies that the space-agent
// subcommand returns a usage error when fewer than 2 arguments are given.
func TestSpaceAgentSubcommand_MissingArgs(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	// No ref, no brief.
	err := runHerdrPlugin(context.Background(), []string{"space-agent"}, out)
	if err == nil {
		t.Fatal("space-agent with no args: expected error, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}

	// Ref only, no brief.
	err = runHerdrPlugin(context.Background(), []string{"space-agent", "myproj/mybox"}, out)
	if err == nil {
		t.Fatal("space-agent with ref only: expected error, got nil")
	}
	if !errors.As(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// TestSpaceAgentFromFileSubcommand_Routes verifies that space-agent-from-file
// is recognised in the dispatch switch (does not return "unknown subcommand").
// It will error on newSandboxService (no store configured in this process)
// rather than "unknown subcommand", proving routing is wired.
func TestSpaceAgentFromFileSubcommand_Routes(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"space-agent-from-file"}, out)
	if err == nil {
		// Unexpected success — might mean no sandbox service error in test env.
		return
	}
	if strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("space-agent-from-file not wired in dispatch switch: %v", err)
	}
	// Any other error (e.g. "store: default root") is acceptable — it means
	// routing worked and failed at a later step, not at dispatch.
}

// sandboxGetterWithMount satisfies sandboxGetter and returns a Sandbox with a
// live mount (so herdrShellCwd returns the mount path, not "/root").
type sandboxGetterWithMount struct {
	mountGuestPath string
}

func (g *sandboxGetterWithMount) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{
		LiveMounts: []domain.LiveMount{{GuestPath: g.mountGuestPath}},
	}, nil
}

// sandboxGetterNoMount satisfies sandboxGetter with no mounts (herdrShellCwd → /root).
type sandboxGetterNoMount struct{}

func (g *sandboxGetterNoMount) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}

// sandboxGetterError satisfies sandboxGetter by returning an error.
type sandboxGetterError struct{ err error }

func (g *sandboxGetterError) Get(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, g.err
}

// TestSpaceAgent_RefusesWhenNoMount verifies the early-fail guard: if
// herdrShellCwd returns "/root" (no live mount or volume), herdrPluginSpaceAgent
// must return a UsageError that names the --mount flag, before doing anything
// with herdr or the sandbox lifecycle.
func TestSpaceAgent_RefusesWhenNoMount(t *testing.T) {
	execCalled := false
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "false")
	}
	defer func() { herdrExecCommandContext = old }()

	// Both refusals are reachable through the narrow sandboxGetter seam, and
	// they must NOT say the same thing: a mistyped handle and a sandbox with
	// no mount need different advice.
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

// TestHerdrPaneRun_ArgvShape verifies that herdrPaneRun builds the correct
// herdr argv: ["pane", "run", <paneID>, <text>].
func TestHerdrPaneRun_ArgvShape(t *testing.T) {
	var capturedName string
	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "true") // succeed silently
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

// TestHerdrPaneWaitOutput_ArgvShape verifies that herdrPaneWaitOutput builds
// the correct herdr argv including --match and --timeout.
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

// TestHerdrPaneReportAgent_ArgvShape verifies that herdrPaneReportAgent builds
// the correct argv with --source, --agent, and --state flags.
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

	// Required flags: --source <ref>, --agent <label>, --state <state>.
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

// TestClaudeReadyMatch_IsModeSpecific pins the readiness token to the
// permission mode it was captured in.
//
// This exists because the first implementation used one token for both modes
// and it silently did not work: "? for shortcuts" is only in the default
// mode's footer, so an autonomous agent that was already sitting at its prompt
// waited out the full 90s timeout. A test that only asserted "the constant is
// non-empty" would have stayed green through that entire failure.
func TestClaudeReadyMatch_IsModeSpecific(t *testing.T) {
	autonomous := claudeReadyMatch(true)
	normal := claudeReadyMatch(false)

	if autonomous == normal {
		t.Fatalf("the two permission modes print different footers, so they cannot share a "+
			"readiness token; both returned %q", autonomous)
	}

	// Verbatim footers captured from a live guest pane, claude v2.1.226.
	const (
		normalFooter     = " ⏸ manual mode on · ? for shortcuts · ← for agents"
		autonomousFooter = " ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"
	)
	if !strings.Contains(normalFooter, normal) {
		t.Errorf("default-mode token %q does not appear in the default-mode footer %q", normal, normalFooter)
	}
	if !strings.Contains(autonomousFooter, autonomous) {
		t.Errorf("autonomous token %q does not appear in the autonomous footer %q", autonomous, autonomousFooter)
	}
	// The cross terms are the actual bug: each token must NOT match the other
	// mode, or the wait reports ready in a mode it was never validated for.
	if strings.Contains(autonomousFooter, normal) {
		t.Errorf("default-mode token %q also matches the autonomous footer; the modes are not distinguished", normal)
	}
	if strings.Contains(normalFooter, autonomous) {
		t.Errorf("autonomous token %q also matches the default-mode footer; the modes are not distinguished", autonomous)
	}

	// No token may match a first-run wizard, or the wait reports ready while
	// claude is still on a dialog. "❯" is the trap: it is the prompt glyph AND
	// every wizard's selector glyph.
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

// TestHerdrPaneSubmitToAgent_SendsTextThenEnterSeparately pins the two-call
// submit.
//
// `herdr pane run` sends text and Enter in one call and is correct for a shell
// prompt. Against claude's TUI it is not: observed live, the brief landed in
// the input box and stayed there unsubmitted until an Enter was sent
// separately. Collapsing this back into one `pane run` call produces an agent
// that starts, prompts, and never acts — a failure invisible in the argv.
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

// TestGuestAgentLaunchCommand_DoesNotDependOnTheShellFunction pins the launch
// command to being self-contained.
//
// SeedGuestShellProfile installs a `claude` shell function that adds
// --dangerously-skip-permissions automatically, which makes it tempting to
// launch with a bare "claude" and let the profile supply the flag. That is a
// race, and it was observed failing live: the pane's login shell sources
// /etc/profile.d asynchronously, so a command typed before that completes
// resolves `claude` to the raw binary. The agent then starts in the DEFAULT
// permission mode and prints the default footer, so the autonomous readiness
// wait times out against an agent that is running perfectly well — a 90-second
// failure with no error message pointing anywhere near the cause.
func TestGuestAgentLaunchCommand_DoesNotDependOnTheShellFunction(t *testing.T) {
	const flag = "--dangerously-skip-permissions"

	autonomous := guestAgentLaunchCommand(true)
	if !strings.Contains(autonomous, flag) {
		t.Errorf("autonomous launch must pass %s explicitly rather than relying on the shell "+
			"function, which may not be loaded yet; got %q", flag, autonomous)
	}
	if !strings.Contains(autonomous, "IS_SANDBOX=1") {
		t.Errorf("autonomous launch must set IS_SANDBOX=1: claude refuses %s as root without it; got %q",
			flag, autonomous)
	}

	normal := guestAgentLaunchCommand(false)
	if strings.Contains(normal, flag) {
		t.Errorf("non-autonomous launch must not pass %s; got %q", flag, normal)
	}
	// It must also bypass the shell function, or the profile silently re-adds
	// the flag and the non-autonomous mode does not exist in practice.
	if !strings.HasPrefix(normal, "command ") {
		t.Errorf("non-autonomous launch must use `command claude` to bypass the shell function "+
			"that would otherwise re-add %s; got %q", flag, normal)
	}

	// The readiness token is chosen by mode, so the two must stay in step: the
	// mode the command actually produces determines the footer that is matched.
	if claudeReadyMatch(true) == claudeReadyMatch(false) {
		t.Error("readiness tokens for the two modes collapsed; see claudeReadyMatch")
	}
}

// ── J2: space-agent --no-focus propagates through herdrOpenGuestShellPane ────
//
// Both tests call the production herdrOpenGuestShellPane directly — they do
// not restate the condition inline. The mutation target is the focus branch
// inside herdrOpenGuestShellPane: inverting it makes both tests RED.

// TestSpaceAgent_PaneOpenFocusArgv_WithFocus asserts that focus=true produces
// --focus (and no --no-focus) in the herdr pane open argv. This pins the
// default interactive path: a single space-agent run must bring the new pane
// into view without the operator having to click.
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

// TestSpaceAgent_PaneOpenFocusArgv_WithNoFocus asserts that focus=false
// produces --no-focus (and no --focus) in the herdr pane open argv. This is
// the N-way concurrent path: AC-5 requires that a second concurrent
// space-agent does not steal focus from the first.
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

// TestSpaceAgentSubcommand_NoFocusFlagParsed verifies that --no-focus is
// accepted before the sandbox ref and does not produce an "unknown flag"
// usage error. The call fails later (no real sandbox), but the flag must be
// consumed without complaint.
func TestSpaceAgentSubcommand_NoFocusFlagParsed(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"space-agent", "--no-focus", "proj/x", "do something"}, out)
	if err == nil {
		t.Fatal("expected an error (no real sandbox), got nil")
	}
	// The "unknown flag" guard produces a UsageError containing the flag name.
	// Any other error (including a UsageError about a missing sandbox) proves
	// the flag was consumed correctly by the parser.
	if ue, ok := err.(*UsageError); ok && strings.Contains(ue.Msg, "--no-focus") {
		t.Errorf("--no-focus was not parsed; got usage error: %v", ue)
	}
}

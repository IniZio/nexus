package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scripts/watch-pane.sh cannot be run against a live guest from inside a
// sandbox — there is no second VM to dispatch into and herdr is not reachable.
// Its LOGIC is testable regardless, because every herdr call goes through the
// HERDR_BIN seam. These tests stand up a stub herdr that replays recorded pane
// transcripts and drive the real script over it.
//
// The trap the skill names: "always WORKING" passes every positive test there
// is. TestWatchPane_IdleTranscriptExitsIdle is the negative test that a
// stuck-on-WORKING implementation fails — it never exits, so it dies on the
// timeout instead of returning AGENT_IDLE.

// paneWorkingA and paneWorkingB are the same working pane one spinner tick
// apart. Only the elapsed timer moved. This is the movement the detector
// decides on, and it is ALL that separates these two reads.
const paneWorkingA = `● Read(internal/cli/cmd_herdr_plugin.go)
  ⎿  Read 240 lines

✻ Herding… (41s · ↑ 8.2k tokens · esc to interrupt)
`

const paneWorkingB = `● Read(internal/cli/cmd_herdr_plugin.go)
  ⎿  Read 240 lines

✻ Herding… (42s · ↑ 8.2k tokens · esc to interrupt)
`

// paneWorkingNoAffordance is the skill's false-stop mode 2: an agent blocked on
// its own background subagent. It is working, but it is not itself running a
// tool, so the interrupt affordance is GONE — permanently, no amount of
// re-sampling brings it back. Only movement can see this one.
const paneWorkingNoAffordanceA = `● Delegating to 3 subagents.

  nexus3-slice-sandbox   running   0:41
`

const paneWorkingNoAffordanceB = `● Delegating to 3 subagents.

  nexus3-slice-sandbox   running   0:42
`

// paneIdle is a finished agent at its prompt: static, no affordance, no
// question. The only correct verdict is AGENT_IDLE.
const paneIdle = `● Done. Committed as 9fb9b93.

╭──────────────────────────────────────────────╮
│ >                                            │
╰──────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle)
`

// paneQuestion is a stopped agent awaiting an answer.
const paneQuestion = `● I need to know which branch to target.

  Do you want to proceed?
  ❯ 1. Yes
    2. No, tell me more
`

// watchPaneEnv stands up a stub herdr whose `pane read` replays a transcript
// sequence and whose `pane get` reports a scroll offset.
type watchPaneEnv struct {
	dir       string
	herdrBin  string
	readsFile string
}

// newWatchPaneEnv writes a stub herdr. `pane read` emits the Nth transcript
// from reads (repeating the last once exhausted) using a counter file, so the
// stub is stateless between invocations exactly as a real CLI is.
//
// scrollOffset is what `pane get` reports. The literal "ERROR" makes `pane get`
// exit non-zero, and "MISSING" makes it emit JSON with no offset_from_bottom
// field — the two indeterminate cases that must bias to WORKING.
//
// cycle decides what happens once the transcripts run out. CLAMP (false) repeats
// the last one forever, which is how a pane that has genuinely settled behaves.
// CYCLE (true) wraps to the start, which is how a pane that keeps repainting
// behaves. Getting this backwards makes an endlessly-working fixture go static
// the moment the list is exhausted, and the test then measures the fixture
// rather than the script.
func newWatchPaneEnv(t *testing.T, reads []string, scrollOffset string, cycle bool) *watchPaneEnv {
	t.Helper()
	dir := t.TempDir()

	readsDir := filepath.Join(dir, "reads")
	if err := os.MkdirAll(readsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, r := range reads {
		if err := os.WriteFile(filepath.Join(readsDir, fmt.Sprintf("%d", i)), []byte(r), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var scrollBody string
	switch scrollOffset {
	case "ERROR":
		scrollBody = `exit 1`
	case "MISSING":
		scrollBody = `echo '{"result":{"pane":{"pane_id":"w7M:p2","scroll":{}}}}'`
	default:
		scrollBody = fmt.Sprintf(`echo '{"result":{"pane":{"pane_id":"w7M:p2","scroll":{"offset_from_bottom":%s}}}}'`, scrollOffset)
	}

	stub := fmt.Sprintf(`#!/bin/sh
# stub herdr
READS=%q
N=%d
CYCLE=%q
CTR="$READS/../counter"
case "$1 $2" in
  "pane read")
    # Only --source visible answers; --source recent-unwrapped returns EMPTY,
    # reproducing the real trap on a pane that has not scrolled yet.
    case "$*" in
      *recent-unwrapped*) exit 0 ;;
    esac
    i=0
    [ -f "$CTR" ] && i=$(cat "$CTR")
    next=$((i + 1))
    echo "$next" > "$CTR"
    if [ "$CYCLE" = "1" ]; then
      i=$((i %% N))
    else
      [ "$i" -ge "$N" ] && i=$((N - 1))
    fi
    cat "$READS/$i"
    ;;
  "pane get")
    %s
    ;;
  *)
    exit 0
    ;;
esac
`, readsDir, len(reads), map[bool]string{true: "1", false: "0"}[cycle], scrollBody)

	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return &watchPaneEnv{dir: dir, herdrBin: bin, readsFile: readsDir}
}

// run executes the real scripts/watch-pane.sh against the stub, with the poll
// constants collapsed so the test finishes in seconds.
func (e *watchPaneEnv) run(t *testing.T, timeout time.Duration) (stdout string, code int, timedOut bool) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "watch-pane.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(script); statErr != nil {
		t.Fatalf("scripts/watch-pane.sh is missing: %v", statErr)
	}

	cmd := exec.Command("sh", script, "w7M:p2")
	cmd.Env = append(os.Environ(),
		"HERDR_BIN="+e.herdrBin,
		"WATCH_PANE_MOVEMENT_GAP=0",
		"WATCH_PANE_POLL_INTERVAL=0",
		"WATCH_PANE_START_GRACE=2",
		"WATCH_PANE_SETTLE_ROUNDS=1",
	)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				return buf.String(), ee.ExitCode(), false
			}
			t.Fatalf("run watch-pane.sh: %v", waitErr)
		}
		return buf.String(), 0, false
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return buf.String(), -1, true
	}
}

const (
	exitAgentIdle         = 10
	exitAgentQuestion     = 11
	exitAgentNeverStarted = 12
	exitRefused           = 3
)

// TestWatchPane_IdleTranscriptExitsIdle is THE NEGATIVE TEST.
//
// The pane works for a few reads and then goes static with no affordance and no
// question. The script must exit AGENT_IDLE. An "always WORKING" implementation
// — which passes every positive test in this file — never leaves the watch loop
// and is killed on the timeout, so this test is the one that fails it.
func TestWatchPane_IdleTranscriptExitsIdle(t *testing.T) {
	env := newWatchPaneEnv(t, []string{
		paneWorkingA, paneWorkingB, // start grace: movement → WORKING
		paneIdle, paneIdle, // round 1: static → not working
		paneIdle, paneIdle, // settle round
		paneIdle, // final read for classification
	}, "0", false)

	out, code, timedOut := env.run(t, 20*time.Second)
	if timedOut {
		t.Fatalf("watch-pane.sh never exited on an idle pane — this is the always-WORKING failure.\noutput:\n%s", out)
	}
	if code != exitAgentIdle {
		t.Errorf("exit code = %d, want %d (AGENT_IDLE)\noutput:\n%s", code, exitAgentIdle, out)
	}
	if !strings.Contains(out, "AGENT_IDLE") {
		t.Errorf("output does not name the reason:\n%s", out)
	}
}

// TestWatchPane_WorkingTranscriptDoesNotStop is the positive test: a pane that
// keeps moving must NOT produce a stop verdict. It is expected to time out —
// blocking is the correct behaviour — so the timeout here is the PASS.
func TestWatchPane_WorkingTranscriptDoesNotStop(t *testing.T) {
	// Two transcripts one spinner tick apart, CYCLED: the pane repaints forever,
	// exactly as a working agent's does.
	env := newWatchPaneEnv(t, []string{paneWorkingA, paneWorkingB}, "0", true)

	out, code, timedOut := env.run(t, 6*time.Second)
	if !timedOut {
		t.Errorf("watch-pane.sh reported a stop (exit %d) on a pane that never stopped moving\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "agent is working") {
		t.Errorf("start-grace loop did not detect the working agent:\n%s", out)
	}
}

// TestWatchPane_BlockedOnSubagentIsWorking covers the skill's false-stop mode
// 2: an agent waiting on its own background subagent shows NO interrupt
// affordance at all, ever. Only movement can see it. A detector that treated
// the affordance's absence as idleness stops here immediately.
func TestWatchPane_BlockedOnSubagentIsWorking(t *testing.T) {
	env := newWatchPaneEnv(t, []string{paneWorkingNoAffordanceA, paneWorkingNoAffordanceB}, "0", true)

	out, code, timedOut := env.run(t, 6*time.Second)
	if !timedOut {
		t.Errorf("reported a stop (exit %d) on an agent blocked on its own subagent — "+
			"the interrupt affordance's ABSENCE was treated as evidence\noutput:\n%s", code, out)
	}
}

// TestWatchPane_QuestionDiscriminated proves strings still do their one job:
// separating a stopped-on-a-question pane from a stopped-and-done one. Both are
// static; movement cannot tell them apart.
func TestWatchPane_QuestionDiscriminated(t *testing.T) {
	env := newWatchPaneEnv(t, []string{
		paneWorkingA, paneWorkingB,
		paneQuestion, paneQuestion,
		paneQuestion, paneQuestion,
		paneQuestion,
	}, "0", false)

	out, code, timedOut := env.run(t, 20*time.Second)
	if timedOut {
		t.Fatalf("never exited on a question pane\noutput:\n%s", out)
	}
	if code != exitAgentQuestion {
		t.Errorf("exit code = %d, want %d (AGENT_QUESTION)\noutput:\n%s", code, exitAgentQuestion, out)
	}
	if !strings.Contains(out, "RESTART this watcher") {
		t.Errorf("question verdict does not tell the operator to restart the watcher:\n%s", out)
	}
}

// TestWatchPane_ScrolledPaneReadsAsWorking is the FIFTH false-stop mode. The
// transcript is a genuinely STATIC idle pane; the only thing that differs is
// that the pane is scrolled up, which is exactly the state in which a static
// read proves nothing — the spinner is outside the window.
//
// Sub-tests cover all three ways the scroll answer can be indeterminate. Every
// one must bias to WORKING.
func TestWatchPane_ScrolledPaneReadsAsWorking(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset string
	}{
		{"definitely scrolled up", "40"},
		{"herdr pane get fails", "ERROR"},
		{"offset_from_bottom field absent", "MISSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A single, genuinely STATIC transcript: clamped, so every read is
			// identical. Only the scroll answer differs between sub-tests.
			env := newWatchPaneEnv(t, []string{paneIdle}, tc.offset, false)

			out, code, timedOut := env.run(t, 6*time.Second)
			if !timedOut {
				t.Errorf("reported a stop (exit %d) on a scrolled pane — a static read from "+
					"outside the spinner's window is not evidence of a stop\noutput:\n%s", code, out)
			}
		})
	}
}

// TestWatchPane_ScrolledAtBottomStillStops is the mirror of the above, and the
// reason it matters: the scroll guard must not become a second always-WORKING.
// offset_from_bottom = 0 is a DEFINITE "at the bottom", so a static pane there
// really is stopped.
func TestWatchPane_ScrolledAtBottomStillStops(t *testing.T) {
	env := newWatchPaneEnv(t, []string{
		paneWorkingA, paneWorkingB,
		paneIdle, paneIdle,
		paneIdle, paneIdle,
		paneIdle,
	}, "0", false)

	out, code, timedOut := env.run(t, 20*time.Second)
	if timedOut {
		t.Fatalf("scroll guard swallowed a real stop at offset 0\noutput:\n%s", out)
	}
	if code != exitAgentIdle {
		t.Errorf("exit code = %d, want %d\noutput:\n%s", code, exitAgentIdle, out)
	}
}

// TestWatchPane_UnreadablePaneRefuses is the fail-closed rail. Both pane-read
// sources empty means no verdict is obtainable, and the script must say so
// rather than calling it a stop — a false AGENT_IDLE sends the orchestrator to
// review work that does not exist.
func TestWatchPane_UnreadablePaneRefuses(t *testing.T) {
	env := newWatchPaneEnv(t, []string{""}, "0", false)

	out, code, timedOut := env.run(t, 20*time.Second)
	if timedOut {
		t.Fatalf("hung on an unreadable pane\noutput:\n%s", out)
	}
	if code != exitRefused {
		t.Errorf("exit code = %d, want %d (REFUSED)\noutput:\n%s", code, exitRefused, out)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Errorf("did not name the refusal:\n%s", out)
	}
}

// TestWatchPane_NeverStartedIsItsOwnVerdict pins the diagnostic distinction the
// skill draws: an agent that never began is not an agent that stopped. The
// start-grace loop must call THE SAME is_working the watch loop calls — bug 3
// in the skill's list was a second inlined copy that kept reporting
// AGENT_NEVER_STARTED after the shared one had learned better.
func TestWatchPane_NeverStartedIsItsOwnVerdict(t *testing.T) {
	env := newWatchPaneEnv(t, []string{paneIdle}, "0", false)

	out, code, timedOut := env.run(t, 20*time.Second)
	if timedOut {
		t.Fatalf("hung instead of reporting AGENT_NEVER_STARTED\noutput:\n%s", out)
	}
	if code != exitAgentNeverStarted {
		t.Errorf("exit code = %d, want %d (AGENT_NEVER_STARTED)\noutput:\n%s",
			code, exitAgentNeverStarted, out)
	}
	if strings.Contains(out, "AGENT_IDLE") {
		t.Errorf("reported AGENT_IDLE for an agent that never started:\n%s", out)
	}
}

// TestWatchPane_OneWorkingDefinition is a source guard for the skill's rule
// that there is ONE definition of "working" called by every loop. It is here
// because the drift it prevents is invisible to behavioural tests until the two
// copies have already diverged — which is exactly when the tests were written
// against the copy that was still right.
func TestWatchPane_OneWorkingDefinition(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "watch-pane.sh"))
	if err != nil {
		t.Fatalf("read watch-pane.sh: %v", err)
	}
	body := string(src)

	if n := strings.Count(body, "\nis_working() {"); n != 1 {
		t.Errorf("found %d definitions of is_working(); want exactly 1", n)
	}
	// Both loops must call it. Two call sites, one definition.
	if n := strings.Count(body, "\n    is_working\n"); n != 2 {
		t.Errorf("is_working is called from %d loops; want 2 (start-grace and watch)", n)
	}
	// The question discriminator must not be consulted to decide working-ness.
	// Scoped to is_working's BODY: the header comment legitimately discusses
	// classify_question, and a guard that trips on prose is a guard nobody keeps.
	start := strings.Index(body, "\nis_working() {")
	if start < 0 {
		t.Fatal("is_working() not found")
	}
	end := strings.Index(body[start+1:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of is_working()")
	}
	isWorkingBody := body[start+1 : start+1+end]
	if strings.Contains(isWorkingBody, "classify_question") {
		t.Error("is_working consults classify_question — strings have become the " +
			"working test instead of the question test")
	}
}

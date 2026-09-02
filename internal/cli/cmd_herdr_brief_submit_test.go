package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Transcripts below are the two states observed live on 2026-09-02 while
// dispatching a three-slice wave through `nexus3 herdr agent --autonomous
// --no-focus`. Two briefs submitted; the third stranded. The CLI reported
// success on all three.
//
// Note what is IDENTICAL in both: the footer line
// "⏵⏵ bypass permissions on (shift+tab to cycle)". That is claudeReadyMatch's
// token for the autonomous path — present before the paste, after the paste,
// and after submission alike. The dispatch waited on it and called that
// delivery. It is the marker that proves nothing, and both fixtures carry it so
// any classifier that leans on it fails here.

// paneStranded is pane w7P:p2 after the brief was pasted and Enter was pressed
// but never took: the input box still renders the paste placeholder and the
// footer offers to expand it. No working indicator anywhere.
const paneStranded = `● I'll start by reading the actual state of things.

╭──────────────────────────────────────────────────────────────╮
│ > [Pasted text #1 +79 lines]                                 │
╰──────────────────────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle) · paste again to expand
`

// paneSubmitted is a sibling pane from the same wave whose brief DID submit:
// the input box is empty and the agent is working.
const paneSubmitted = `● I'll start by reading the actual state of things.

● Read(internal/cli/cmd_herdr_plugin.go)
  ⎿  Read 240 lines

✻ Thinking… (12s · ↑ 3.1k tokens · esc to interrupt)

╭──────────────────────────────────────────────────────────────╮
│ >                                                            │
╰──────────────────────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle)
`

// paneSubmittedTick is paneSubmitted one second later: only the spinner's
// elapsed timer moved. This is the movement signal the classifier decides on.
var paneSubmittedTick = strings.Replace(paneSubmitted, "12s", "13s", 1)

// paneIdleAtPrompt is a submitted-and-finished agent: empty box, no working
// indicator, no stranded marker, and static across reads. There is genuinely no
// evidence either way here, so the classifier must say UNKNOWN — not
// SUBMITTED. This is the fixture that catches a fail-open rewrite.
const paneIdleAtPrompt = `● Done. The change is in internal/cli/cmd_herdr_plugin.go.

╭──────────────────────────────────────────────────────────────╮
│ >                                                            │
╰──────────────────────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle)
`

// TestClassifyBriefSubmission_LiveTranscripts drives the classifier over the
// observed pane states.
func TestClassifyBriefSubmission_LiveTranscripts(t *testing.T) {
	cases := []struct {
		name               string
		before, after      string
		beforeOK, afterOK  bool
		want               briefSubmissionVerdict
		wantReasonContains string
	}{
		{
			name:               "stranded: paste placeholder still in the input box",
			before:             paneStranded,
			after:              paneStranded,
			beforeOK:           true,
			afterOK:            true,
			want:               briefSubmissionStranded,
			wantReasonContains: "Pasted text",
		},
		{
			name: "stranded beats movement: a stranded pane still repaints",
			// The placeholder blinks and the footer cycles, so a stranded pane
			// is NOT static. If movement were checked first this would pass as
			// submitted — which is the original defect wearing a new hat.
			before:             paneStranded,
			after:              strings.Replace(paneStranded, "+79 lines", "+79 lines ", 1),
			beforeOK:           true,
			afterOK:            true,
			want:               briefSubmissionStranded,
			wantReasonContains: "Pasted text",
		},
		{
			name:               "submitted: pane repainted between reads",
			before:             paneSubmitted,
			after:              paneSubmittedTick,
			beforeOK:           true,
			afterOK:            true,
			want:               briefSubmissionSubmitted,
			wantReasonContains: "repainted",
		},
		{
			name: "submitted fast path: static but shows the interrupt affordance",
			// A working agent between repaints. Movement is absent; the
			// affordance carries it.
			before:             paneSubmitted,
			after:              paneSubmitted,
			beforeOK:           true,
			afterOK:            true,
			want:               briefSubmissionSubmitted,
			wantReasonContains: "esc to interrupt",
		},
		{
			name:               "unknown: static, no working indicator, no stranded marker",
			before:             paneIdleAtPrompt,
			after:              paneIdleAtPrompt,
			beforeOK:           true,
			afterOK:            true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "static",
		},
		{
			name:               "unknown: first read unobtainable",
			before:             "",
			after:              paneSubmitted,
			beforeOK:           false,
			afterOK:            true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "no text",
		},
		{
			name:               "unknown: second read unobtainable",
			before:             paneSubmitted,
			after:              "",
			beforeOK:           true,
			afterOK:            false,
			want:               briefSubmissionUnknown,
			wantReasonContains: "no text",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := classifyBriefSubmission(tc.before, tc.after, tc.beforeOK, tc.afterOK)
			if got != tc.want {
				t.Errorf("classifyBriefSubmission = %s (%s); want %s", got, reason, tc.want)
			}
			if !strings.Contains(reason, tc.wantReasonContains) {
				t.Errorf("reason = %q; want it to contain %q", reason, tc.wantReasonContains)
			}
		})
	}
}

// TestClassifyBriefSubmission_ReadyTokenIsNotEvidence pins the specific marker
// that caused the defect. "shift+tab to cycle" is what step 7 waits on, and it
// is present in BOTH the stranded and the submitted transcript. A classifier
// that treats it as delivery evidence would call the stranded pane submitted.
func TestClassifyBriefSubmission_ReadyTokenIsNotEvidence(t *testing.T) {
	const readyToken = "shift+tab to cycle"
	if !strings.Contains(paneStranded, readyToken) || !strings.Contains(paneSubmitted, readyToken) {
		t.Fatalf("fixtures no longer both carry %q — this test has stopped testing anything", readyToken)
	}
	for _, m := range briefWorkingMarkers {
		if strings.Contains(paneStranded, m) {
			t.Errorf("working marker %q matches the STRANDED transcript; it cannot discriminate", m)
		}
	}
	got, reason := classifyBriefSubmission(paneStranded, paneStranded, true, true)
	if got != briefSubmissionStranded {
		t.Errorf("stranded pane classified %s (%s); want STRANDED", got, reason)
	}
}

// readStep is one scripted pane read: the transcript it yields and whether the
// read succeeded at all.
type readStep struct {
	text string
	ok   bool
}

// scriptedPaneReader returns a herdrPaneReadFn stub that yields the given
// transcripts in order, repeating the last one once exhausted.
func scriptedPaneReader(reads []readStep, calls *int) func(context.Context, string, string) (string, bool) {
	return func(context.Context, string, string) (string, bool) {
		i := *calls
		*calls++
		if i >= len(reads) {
			i = len(reads) - 1
		}
		return reads[i].text, reads[i].ok
	}
}

// stubHerdrExec swaps herdrExecCommandContext for a recorder that always
// succeeds, so send-text / send-keys do not try to run a real herdr.
func stubHerdrExec(t *testing.T, argv *[][]string) {
	t.Helper()
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		*argv = append(*argv, args)
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { herdrExecCommandContext = old })
}

// stubPaneRead swaps the pane-read seam for a scripted sequence.
func stubPaneRead(t *testing.T, reads []readStep, calls *int) {
	t.Helper()
	old := herdrPaneReadFn
	herdrPaneReadFn = scriptedPaneReader(reads, calls)
	t.Cleanup(func() { herdrPaneReadFn = old })
}

// TestDeliverBriefConfirmed_StrandedFailsLoudly is the regression test for the
// reported defect: a pane whose brief never left the input box must NOT be
// reported as a running agent.
func TestDeliverBriefConfirmed_StrandedFailsLoudly(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{{paneStranded, true}}, &calls)

	var w bytes.Buffer
	err := herdrDeliverBriefConfirmed(context.Background(), "herdr", "w7P:p2", "the brief", &w)
	if err == nil {
		t.Fatal("stranded brief reported success — this is the defect")
	}
	if !strings.Contains(err.Error(), "NOT confirmed submitted") {
		t.Errorf("error does not name the failure: %v", err)
	}
	if !strings.Contains(err.Error(), "w7P:p2") {
		t.Errorf("error does not name the pane the operator must inspect: %v", err)
	}

	// It must have RETRIED Enter, not given up after the first press.
	enters := 0
	for _, a := range argv {
		if len(a) >= 4 && a[0] == "pane" && a[1] == "send-keys" && a[3] == "Enter" {
			enters++
		}
	}
	if enters != briefSubmitAttempts {
		t.Errorf("pressed Enter %d times; want %d (1 initial + %d retries)",
			enters, briefSubmitAttempts, briefSubmitAttempts-1)
	}

	// It must NOT have re-pasted the brief: a second send-text would append to
	// a buffer that already holds it, doubling the prompt.
	sendTexts := 0
	for _, a := range argv {
		if len(a) >= 2 && a[0] == "pane" && a[1] == "send-text" {
			sendTexts++
		}
	}
	if sendTexts != 1 {
		t.Errorf("sent the brief text %d times; want exactly 1", sendTexts)
	}
}

// TestDeliverBriefConfirmed_UnreadablePaneRefuses is the fail-closed rail. A
// pane that cannot be read is not a pane that passes.
func TestDeliverBriefConfirmed_UnreadablePaneRefuses(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{{"", false}}, &calls)

	var w bytes.Buffer
	err := herdrDeliverBriefConfirmed(context.Background(), "herdr", "w7P:p2", "the brief", &w)
	if err == nil {
		t.Fatal("unreadable pane reported success — a check that cannot decide must refuse")
	}
	if !strings.Contains(err.Error(), "UNKNOWN") {
		t.Errorf("error does not carry the UNKNOWN verdict: %v", err)
	}
}

// TestDeliverBriefConfirmed_SubmittedPasses proves the guard is not simply
// stuck on "fail" — the mirror of the always-WORKING trap.
func TestDeliverBriefConfirmed_SubmittedPasses(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{
		{paneSubmitted, true},
		{paneSubmittedTick, true},
	}, &calls)

	var w bytes.Buffer
	if err := herdrDeliverBriefConfirmed(context.Background(), "herdr", "w7P:p2", "the brief", &w); err != nil {
		t.Fatalf("submitted brief rejected: %v", err)
	}
	if !strings.Contains(w.String(), "confirmed on attempt 1") {
		t.Errorf("did not confirm on the first attempt; log was:\n%s", w.String())
	}
	enters := 0
	for _, a := range argv {
		if len(a) >= 4 && a[1] == "send-keys" && a[3] == "Enter" {
			enters++
		}
	}
	if enters != 1 {
		t.Errorf("pressed Enter %d times on an already-submitted brief; want 1", enters)
	}
}

// TestDeliverBriefConfirmed_RetryRecovers covers the middle case: the first
// Enter stranded, the retry took. The dispatch should succeed and say so.
func TestDeliverBriefConfirmed_RetryRecovers(t *testing.T) {
	var argv [][]string
	stubHerdrExec(t, &argv)
	calls := 0
	stubPaneRead(t, []readStep{
		{paneStranded, true},      // round 1 before
		{paneStranded, true},      // round 1 after  → STRANDED, retry Enter
		{paneSubmitted, true},     // round 2 before
		{paneSubmittedTick, true}, // round 2 after → SUBMITTED
	}, &calls)

	var w bytes.Buffer
	if err := herdrDeliverBriefConfirmed(context.Background(), "herdr", "w7P:p2", "the brief", &w); err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if !strings.Contains(w.String(), "confirmed on attempt 2") {
		t.Errorf("expected confirmation on attempt 2; log was:\n%s", w.String())
	}
}

// TestSpaceAgentDispatch_UsesConfirmedDelivery pins the CALL SITE.
//
// The classifier and the retry loop can both be perfect and the defect still
// ship, if herdrPluginSpaceAgent goes on calling the unconfirmed
// herdrPaneSubmitToAgent directly. That function is reachable only through a
// real *service.Service and a real store, so this is asserted against the
// source: step 8 of the dispatch must delegate to herdrDeliverBriefConfirmed,
// and must not paste-and-hope.
//
// herdrPaneSubmitToAgent is NOT banned outright — herdrDeliverBriefConfirmed
// calls it, which is the one legitimate call site. The guard is scoped to the
// body of herdrPluginSpaceAgent.
func TestSpaceAgentDispatch_UsesConfirmedDelivery(t *testing.T) {
	src, err := os.ReadFile("cmd_herdr_plugin.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := spaceAgentFuncBody(t, string(src))

	if !strings.Contains(body, "herdrDeliverBriefConfirmed(") {
		t.Error("herdrPluginSpaceAgent does not call herdrDeliverBriefConfirmed — " +
			"the brief is delivered without confirming it was submitted")
	}
	if strings.Contains(body, "herdrPaneSubmitToAgent(") {
		t.Error("herdrPluginSpaceAgent calls herdrPaneSubmitToAgent directly — " +
			"that path pastes, presses Enter, and reports success without looking")
	}
}

// spaceAgentFuncBody slices out the body of func herdrPluginSpaceAgent.
func spaceAgentFuncBody(t *testing.T, src string) string {
	t.Helper()
	const marker = "\nfunc herdrPluginSpaceAgent(ctx context.Context"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatal("herdrPluginSpaceAgent not found — this guard has stopped guarding anything")
	}
	rest := src[start+1:]
	// The function ends at the first line that is exactly "}" at column 0.
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of herdrPluginSpaceAgent")
	}
	return rest[:end]
}

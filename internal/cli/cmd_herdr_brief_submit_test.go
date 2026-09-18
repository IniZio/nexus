package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// paneStranded: box holds paste placeholder, no working indicator (2026-09-02 live capture).
const paneStranded = `● I'll start by reading the actual state of things.

╭──────────────────────────────────────────────────────────────╮
│ > [Pasted text #1 +79 lines]                                 │
╰──────────────────────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle) · paste again to expand
`

const paneSubmitted = `● I'll start by reading the actual state of things.

● Read(internal/cli/cmd_herdr_plugin.go)
  ⎿  Read 240 lines

✻ Thinking… (12s · ↑ 3.1k tokens · esc to interrupt)

╭──────────────────────────────────────────────────────────────╮
│ >                                                            │
╰──────────────────────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle)
`

var paneSubmittedTick = strings.Replace(paneSubmitted, "12s", "13s", 1)

const paneSubmittedWithChip = "" +
	"❯ [Pasted text #1 +13 lines]\n" +
	"\n" +
	"● Create(/workspace/hello.txt)\n" +
	"  ⎿  Created /workspace/hello.txt\n" +
	"\n" +
	"────────────────────────────────────────────────────────────────────────────\n" +
	"❯\n" +
	"────────────────────────────────────────────────────────────────────────────\n" +
	"  ⏸ manual mode on · ? for shortcuts\n"

const paneNewStyleWithSidePanel = "" +
	"                                                      ────────────────────────────────────────────────────────\n" +
	"❯ --no-focus Create a file named hello.txt            hello.txt (untracked)\n" +
	"                                                      ────────────────────────────────────────────────────────\n" +
	"● Done. Created /workspace/hello.txt\n" +
	"\n" +
	"────────────────────────────────────────────────────────────────────────────────────\n" +
	"❯\n" +
	"────────────────────────────────────────────────────────────────────────────────────\n" +
	"  ⏸ manual mode on · ? for shortcuts\n"

const paneIdleAtPrompt = `● Done. The change is in internal/cli/cmd_herdr_plugin.go.

╭──────────────────────────────────────────────────────────────╮
│ >                                                            │
╰──────────────────────────────────────────────────────────────╯
  ⏵⏵ bypass permissions on (shift+tab to cycle)
`

func TestClassifyBriefSubmission_LiveTranscripts(t *testing.T) {
	cases := []struct {
		name               string
		before, after      string
		afterVisible       string
		beforeOK, afterOK  bool
		afterVisibleOK     bool
		want               briefSubmissionVerdict
		wantReasonContains string
	}{
		{
			name:               "stranded: paste placeholder still in the input box",
			before:             paneStranded,
			after:              paneStranded,
			afterVisible:       paneStranded,
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionStranded,
			wantReasonContains: "Pasted text",
		},
		{
			name:               "stranded beats movement: a stranded pane still repaints",
			before:             paneStranded,
			after:              strings.Replace(paneStranded, "+79 lines", "+79 lines ", 1),
			afterVisible:       strings.Replace(paneStranded, "+79 lines", "+79 lines ", 1),
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionStranded,
			wantReasonContains: "Pasted text",
		},
		{
			name:               "submitted: pane repainted between reads",
			before:             paneSubmitted,
			after:              paneSubmittedTick,
			afterVisible:       paneSubmittedTick,
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionSubmitted,
			wantReasonContains: "repainted",
		},
		{
			name:               "submitted fast path: static but shows the interrupt affordance",
			before:             paneSubmitted,
			after:              paneSubmitted,
			afterVisible:       paneSubmitted,
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionSubmitted,
			wantReasonContains: "esc to interrupt",
		},
		{
			name:               "unknown: static, no working indicator, no stranded marker",
			before:             paneIdleAtPrompt,
			after:              paneIdleAtPrompt,
			afterVisible:       paneIdleAtPrompt,
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "static",
		},
		{
			name:               "unknown: first read unobtainable",
			before:             "",
			after:              paneSubmitted,
			afterVisible:       paneSubmitted,
			beforeOK:           false,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "no text",
		},
		{
			name:               "unknown: second read unobtainable",
			before:             paneSubmitted,
			after:              "",
			afterVisible:       "",
			beforeOK:           true,
			afterOK:            false,
			afterVisibleOK:     false,
			want:               briefSubmissionUnknown,
			wantReasonContains: "no text",
		},
		{
			name:               "scrollback chip does not strand an accepted brief",
			before:             paneSubmitted,
			after:              paneStranded + "\n" + paneSubmittedTick,
			afterVisible:       paneSubmittedTick,
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionSubmitted,
			wantReasonContains: "repainted",
		},
		{
			name:               "chip in transcript only: brief accepted and agent done",
			before:             paneSubmittedWithChip,
			after:              paneSubmittedWithChip,
			afterVisible:       paneSubmittedWithChip,
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionSubmitted,
			wantReasonContains: "transcript",
		},
		{
			name:               "no input box in visible: unknown",
			before:             paneSubmitted,
			after:              paneSubmitted,
			afterVisible:       "some output without a box",
			beforeOK:           true,
			afterOK:            true,
			afterVisibleOK:     true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "no input box",
		},
		{
			name: "ordering: input-box chip beats transcript chip",
			before: "❯ [Pasted text #1 +79 lines]\n\n● prior work\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #2 +12 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"  ⏵⏵ auto mode on · paste again to expand\n",
			after: "❯ [Pasted text #1 +79 lines]\n\n● prior work\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #2 +12 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"  ⏵⏵ auto mode on · paste again to expand\n",
			afterVisible: "❯ [Pasted text #1 +79 lines]\n\n● prior work\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #2 +12 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"  ⏵⏵ auto mode on · paste again to expand\n",
			beforeOK: true, afterOK: true, afterVisibleOK: true,
			want:               briefSubmissionStranded,
			wantReasonContains: "Pasted text",
		},
		{
			name: "rule below box: prompt guard fires, must not be SUBMITTED",
			afterVisible: "transcript\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #1 +10 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"            ──────────────────────────────────────────────────────────────\n" +
				"  ⏸ auto mode on\n",
			before: "transcript\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #1 +10 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"            ──────────────────────────────────────────────────────────────\n" +
				"  ⏸ auto mode on\n",
			after: "transcript\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #1 +10 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"            ──────────────────────────────────────────────────────────────\n" +
				"  ⏸ auto mode on\n",
			beforeOK: true, afterOK: true, afterVisibleOK: true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "no input box",
		},
		{
			name:         "real capture layout: side-panel rules above idle box",
			before:       paneNewStyleWithSidePanel,
			after:        paneNewStyleWithSidePanel,
			afterVisible: paneNewStyleWithSidePanel,
			beforeOK:     true, afterOK: true, afterVisibleOK: true,
			want:               briefSubmissionUnknown,
			wantReasonContains: "static",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := classifyBriefSubmission(tc.before, tc.after, tc.afterVisible, tc.beforeOK, tc.afterOK, tc.afterVisibleOK)
			if got != tc.want {
				t.Errorf("classifyBriefSubmission = %s (%s); want %s", got, reason, tc.want)
			}
			if !strings.Contains(reason, tc.wantReasonContains) {
				t.Errorf("reason = %q; want it to contain %q", reason, tc.wantReasonContains)
			}
		})
	}
}

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
	got, reason := classifyBriefSubmission(paneStranded, paneStranded, paneStranded, true, true, true)
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

// stubPaneRead swaps the pane-read seam for a scripted sequence and collapses
// the settle gap, which exists to outlast a real pane's repaint period.
func stubPaneRead(t *testing.T, reads []readStep, calls *int) {
	t.Helper()
	old := herdrPaneReadFn
	oldVis := herdrPaneReadVisibleFn
	var lastText string
	base := scriptedPaneReader(reads, calls)
	herdrPaneReadFn = func(ctx context.Context, bin, pane string) (string, bool) {
		text, ok := base(ctx, bin, pane)
		if ok {
			lastText = text
		}
		return text, ok
	}
	herdrPaneReadVisibleFn = func(context.Context, string, string) (string, bool) {
		return lastText, lastText != ""
	}
	oldSettle := briefConfirmSettle
	briefConfirmSettle = time.Millisecond
	t.Cleanup(func() {
		herdrPaneReadFn = old
		herdrPaneReadVisibleFn = oldVis
		briefConfirmSettle = oldSettle
	})
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

func TestBriefInputBoxRegion(t *testing.T) {
	cases := []struct {
		name    string
		visible string
		want    string
	}{
		{
			name:    "stranded pane: region starts at box top border",
			visible: paneStranded,
			want: "╭──────────────────────────────────────────────────────────────╮\n" +
				"│ > [Pasted text #1 +79 lines]                                 │\n" +
				"╰──────────────────────────────────────────────────────────────╯\n" +
				"  ⏵⏵ bypass permissions on (shift+tab to cycle) · paste again to expand\n",
		},
		{
			name:    "submitted pane: region is the empty input box",
			visible: paneSubmitted,
			want: "╭──────────────────────────────────────────────────────────────╮\n" +
				"│ >                                                            │\n" +
				"╰──────────────────────────────────────────────────────────────╯\n" +
				"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n",
		},
		{
			name:    "no box border: returns empty string",
			visible: "some output\nwithout box",
			want:    "",
		},
		{
			name: "new-style separator: region starts at second-to-last rule line",
			visible: "transcript line\n" +
				"──────────────────────────\n" +
				"❯ \n" +
				"──────────────────────────\n" +
				"  ⏵⏵ auto mode on\n",
			want: "──────────────────────────\n" +
				"❯ \n" +
				"──────────────────────────\n" +
				"  ⏵⏵ auto mode on\n",
		},
		{
			name: "new-style separator with side-panel rules: uses last two rules",
			visible: "transcript\n" +
				"──────────────────────────\n" +
				"file.txt\n" +
				"──────────────────────────\n" +
				"──────────────────────────\n" +
				"❯ [Pasted text #1 +10 lines]\n" +
				"──────────────────────────\n" +
				"  ⏵⏵ auto mode · paste again to expand\n",
			want: "──────────────────────────\n" +
				"❯ [Pasted text #1 +10 lines]\n" +
				"──────────────────────────\n" +
				"  ⏵⏵ auto mode · paste again to expand\n",
		},
		{
			name: "rule below box: prompt guard returns empty",
			visible: "transcript\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"❯ [Pasted text #1 +10 lines]\n" +
				"──────────────────────────────────────────────────────────────────────────\n" +
				"            ──────────────────────────────────────────────────────────────\n" +
				"  ⏸ auto mode on\n",
			want: "",
		},
		{
			name:    "real capture: side-panel rules above, box selected correctly",
			visible: paneNewStyleWithSidePanel,
			want: "────────────────────────────────────────────────────────────────────────────────────\n" +
				"❯\n" +
				"────────────────────────────────────────────────────────────────────────────────────\n" +
				"  ⏸ manual mode on · ? for shortcuts\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := briefInputBoxRegion(tc.visible)
			if got != tc.want {
				t.Errorf("briefInputBoxRegion:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

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

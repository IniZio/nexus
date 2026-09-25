package herdragent_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IniZio/nexus/internal/herdragent"
)

// scripter delivers ordered runner responses.
type scripter struct {
	calls []scriptedCall
	pos   int
}

type scriptedCall struct {
	out string
	err error
}

func newScripter(calls ...scriptedCall) *scripter { return &scripter{calls: calls} }

func (s *scripter) Run(_ context.Context, argv ...string) (string, error) {
	if s.pos >= len(s.calls) {
		return "", fmt.Errorf("scripter: exhausted after %d calls; got argv=%v", len(s.calls), argv)
	}
	c := s.calls[s.pos]
	s.pos++
	return c.out, c.err
}

func noop(_ context.Context, _ time.Duration) error { return nil }

func countSleep(max int) func(context.Context, time.Duration) error {
	n := 0
	return func(_ context.Context, _ time.Duration) error {
		n++
		if n > max {
			return context.DeadlineExceeded
		}
		return nil
	}
}

const workingJSON = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"working","pane_id":"w9Z:p1M","revision":5,"state_change_seq":2041,"workspace_id":"w9Z","tab_id":"w9Z:t1M"},"type":"agent_info"}}`
const doneJSON10 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","pane_id":"w9Z:p1M","revision":5,"state_change_seq":10,"workspace_id":"w9Z"},"type":"agent_info"}}`
const doneJSON11 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","pane_id":"w9Z:p1M","revision":5,"state_change_seq":11,"workspace_id":"w9Z"},"type":"agent_info"}}`
const doneJSON12 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","pane_id":"w9Z:p1M","revision":5,"state_change_seq":12,"workspace_id":"w9Z"},"type":"agent_info"}}`
const doneJSON13 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","pane_id":"w9Z:p1M","revision":5,"state_change_seq":13,"workspace_id":"w9Z"},"type":"agent_info"}}`
const blockedJSON11 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"blocked","pane_id":"w9Z:p1M","revision":5,"state_change_seq":11,"workspace_id":"w9Z"},"type":"agent_info"}}`
const idleJSON5 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"idle","pane_id":"w9Z:p1M","revision":5,"state_change_seq":5,"workspace_id":"w9Z"},"type":"agent_info"}}`
const idleJSON6 = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"idle","pane_id":"w9Z:p1M","revision":5,"state_change_seq":6,"workspace_id":"w9Z"},"type":"agent_info"}}`
const notFoundJSON = `{"error":{"code":"agent_not_found","message":"agent not found"}}`

func TestObserve_ParsesAgentGet(t *testing.T) {
	s := newScripter(scriptedCall{out: workingJSON})
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusWorking {
		t.Fatalf("want working, got %q", st.Status)
	}
	if st.Seq != 2041 {
		t.Fatalf("want seq=2041, got %d", st.Seq)
	}
	if st.Agent != "claude" {
		t.Fatalf("want agent=claude, got %q", st.Agent)
	}
	if !st.Settled {
		t.Fatal("want settled=true for working")
	}
}

func TestObserve_BlockedReadsQuestion(t *testing.T) {
	s := newScripter(
		scriptedCall{out: blockedJSON11},
		scriptedCall{out: "Do you want me to proceed?"},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusBlocked {
		t.Fatalf("want blocked, got %q", st.Status)
	}
	if st.Question != "Do you want me to proceed?" {
		t.Fatalf("want question text, got %q", st.Question)
	}
	if !st.Settled {
		t.Fatal("want settled=true for blocked")
	}
}

func TestObserve_DoneThenBlocked_RepollsOnSeq(t *testing.T) {
	s := newScripter(
		scriptedCall{out: doneJSON10},
		scriptedCall{out: blockedJSON11},
		scriptedCall{out: "Are you sure?"},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusBlocked {
		t.Fatalf("want blocked after repoll, got %q", st.Status)
	}
	if st.Question != "Are you sure?" {
		t.Fatalf("want question, got %q", st.Question)
	}
	if st.Seq != 11 {
		t.Fatalf("want seq=11, got %d", st.Seq)
	}
	if !st.Settled {
		t.Fatal("want settled=true")
	}
}

func TestObserve_StableDone_Settled(t *testing.T) {
	s := newScripter(
		scriptedCall{out: doneJSON10},
		scriptedCall{out: doneJSON10},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusDone {
		t.Fatalf("want done, got %q", st.Status)
	}
	if st.Seq != 10 {
		t.Fatalf("want seq=10, got %d", st.Seq)
	}
	if !st.Settled {
		t.Fatal("want settled=true when seq stable")
	}
}

func TestObserve_SeqChurn_BoundedRepolls(t *testing.T) {
	s := newScripter(
		scriptedCall{out: doneJSON10},
		scriptedCall{out: doneJSON11},
		scriptedCall{out: doneJSON12},
		scriptedCall{out: doneJSON13},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Settled {
		t.Fatal("want settled=false when seq keeps advancing past max repolls")
	}
}

func TestObserve_WaitMsUsesAgentWait(t *testing.T) {
	var recorded [][]string
	s := newScripter(
		scriptedCall{out: ""},
		scriptedCall{out: workingJSON},
	)
	recorder := func(ctx context.Context, argv ...string) (string, error) {
		cp := make([]string, len(argv))
		copy(cp, argv)
		recorded = append(recorded, cp)
		return s.Run(ctx, argv...)
	}
	c := herdragent.New(recorder, herdragent.WithSleep(noop))
	c.Observe(context.Background(), "w9Z:p1M", 500*time.Millisecond)

	if len(recorded) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(recorded))
	}
	if len(recorded[0]) < 2 || recorded[0][1] != "wait" {
		t.Errorf("first call should use 'wait' verb, got %v", recorded[0])
	}
	found := false
	for _, a := range recorded[0] {
		if a == "500" {
			found = true
		}
	}
	if !found {
		t.Errorf("timeout 500 not in wait args: %v", recorded[0])
	}
	if len(recorded[1]) < 2 || recorded[1][1] != "get" {
		t.Errorf("second call should use 'get' verb, got %v", recorded[1])
	}
}

func TestObserve_HerdrMissing_Unknown(t *testing.T) {
	s := newScripter(scriptedCall{out: "", err: fmt.Errorf("exec: herdr not found")})
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusUnknown {
		t.Fatalf("want unknown, got %q", st.Status)
	}
	if st.Reason != "herdr_unavailable" {
		t.Fatalf("want reason=herdr_unavailable, got %q", st.Reason)
	}
}

func TestObserve_AgentNotFound_Unknown(t *testing.T) {
	s := newScripter(scriptedCall{out: notFoundJSON, err: fmt.Errorf("exit status 1")})
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusUnknown {
		t.Fatalf("want unknown, got %q", st.Status)
	}
	if st.Reason != "agent_not_found" {
		t.Fatalf("want reason=agent_not_found, got %q", st.Reason)
	}
}

func TestObserve_Malformed_Unknown(t *testing.T) {
	s := newScripter(scriptedCall{out: "not json at all"})
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusUnknown {
		t.Fatalf("want unknown, got %q", st.Status)
	}
	if st.Reason != "parse_error" {
		t.Fatalf("want reason=parse_error, got %q", st.Reason)
	}
}

func TestReady_IdleAtBaselineIsNotReady(t *testing.T) {
	s := newScripter(
		scriptedCall{out: idleJSON5},
		scriptedCall{out: idleJSON5},
		scriptedCall{out: idleJSON5},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(countSleep(2)))
	baseline := herdragent.State{Status: herdragent.StatusIdle, Seq: 5}
	st, ok := c.Ready(context.Background(), "w9Z:p1M", baseline, 5*time.Second)
	if ok {
		t.Fatalf("want ok=false when seq equals baseline, got true with state %+v", st)
	}
}

func TestReady_IdleAfterSeqAdvance(t *testing.T) {
	s := newScripter(
		scriptedCall{out: idleJSON5},
		scriptedCall{out: idleJSON6},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	baseline := herdragent.State{Status: herdragent.StatusIdle, Seq: 5}
	st, ok := c.Ready(context.Background(), "w9Z:p1M", baseline, 5*time.Second)
	if !ok {
		t.Fatalf("want ok=true when seq advances beyond baseline, got false")
	}
	if st.Seq != 6 {
		t.Fatalf("want seq=6, got %d", st.Seq)
	}
}

// Defect 1: tail truncation tests.

func TestObserve_BlockedQuestion_TailTruncated(t *testing.T) {
	// Build output > 4000 bytes where the question is at the tail.
	noise := strings.Repeat("NOISE", 1000) // 5000 bytes
	tail := "QUESTION_AT_TAIL"
	paneOut := noise + "\n" + tail

	s := newScripter(
		scriptedCall{out: blockedJSON11},
		scriptedCall{out: paneOut},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if !strings.Contains(st.Question, tail) {
		t.Errorf("question tail missing; question=%q", st.Question[:min(len(st.Question), 60)])
	}
	if len(st.Question) > 4000 {
		t.Errorf("question exceeds 4000 bytes: %d", len(st.Question))
	}
}

func TestObserve_BlockedQuestion_MultibyteBoundary(t *testing.T) {
	// "A" + "é"(2 bytes) + "B"×3999 = 4002 bytes total.
	input := "A" + "é" + strings.Repeat("B", 3999)
	if len(input) != 4002 {
		t.Fatalf("fixture length wrong: %d", len(input))
	}

	s := newScripter(
		scriptedCall{out: blockedJSON11},
		scriptedCall{out: input},
	)
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)

	if len(st.Question) == 0 {
		t.Fatal("question empty")
	}
	if st.Question[0] != 'B' {
		t.Errorf("want tail content starting with 'B', got 0x%02x", st.Question[0])
	}
	if !utf8.RuneStart(st.Question[0]) {
		t.Errorf("question starts on non-rune-start byte 0x%02x", st.Question[0])
	}
	if !utf8.ValidString(st.Question) {
		t.Error("question is not valid UTF-8")
	}
	if len(st.Question) > 4000 {
		t.Errorf("question exceeds 4000 bytes: %d", len(st.Question))
	}
}

const unknownStatusJSON = `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"unknown","pane_id":"w9Z:p1M","state_change_seq":3,"workspace_id":"w9Z"},"type":"agent_info"}}`

func TestObserve_HerdrStatusUnknown_Undetected(t *testing.T) {
	s := newScripter(scriptedCall{out: unknownStatusJSON})
	c := herdragent.New(s.Run, herdragent.WithSleep(noop))
	st := c.Observe(context.Background(), "w9Z:p1M", 0)
	if st.Status != herdragent.StatusUnknown {
		t.Fatalf("want unknown, got %q", st.Status)
	}
	if st.Reason != "undetected" {
		t.Fatalf("want reason=undetected, got %q", st.Reason)
	}
}

func TestObserve_WaitCancelledCtx_Unknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ctxAwareRunner := func(ctx context.Context, argv ...string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return workingJSON, nil
	}
	c := herdragent.New(ctxAwareRunner, herdragent.WithSleep(noop))
	st := c.Observe(ctx, "w9Z:p1M", 500*time.Millisecond)
	if st.Status != herdragent.StatusUnknown {
		t.Fatalf("want unknown on cancelled ctx, got %q", st.Status)
	}
	if st.Reason != "timeout" {
		t.Fatalf("want reason=timeout, got %q", st.Reason)
	}
}

func TestReady_ConsecutiveUnknown_ExitsEarly(t *testing.T) {
	sleeps := 0
	sleepFn := func(_ context.Context, _ time.Duration) error {
		sleeps++
		return nil
	}
	hardErr := scriptedCall{out: "", err: fmt.Errorf("exec: herdr not found")}
	s := newScripter(hardErr, hardErr, hardErr, hardErr)
	c := herdragent.New(s.Run, herdragent.WithSleep(sleepFn))
	baseline := herdragent.State{Status: herdragent.StatusIdle, Seq: 1}
	st, ok := c.Ready(context.Background(), "w9Z:p1M", baseline, time.Hour)
	if ok {
		t.Fatal("want ok=false on hard consecutive unknowns")
	}
	if s.pos > 3 {
		t.Errorf("want ≤3 get calls, got %d", s.pos)
	}
	if sleeps > 2 {
		t.Errorf("want ≤2 sleeps, got %d", sleeps)
	}
	if st.Status != herdragent.StatusUnknown {
		t.Errorf("want unknown state, got %q", st.Status)
	}
}

func TestReady_HardUnknown_ExitsAfter3(t *testing.T) {
	sleeps := 0
	hardErr := scriptedCall{out: "", err: fmt.Errorf("exec: herdr not found")}
	s := newScripter(hardErr, hardErr, hardErr, hardErr)
	c := herdragent.New(s.Run, herdragent.WithSleep(func(_ context.Context, _ time.Duration) error {
		sleeps++
		return nil
	}))
	baseline := herdragent.State{Status: herdragent.StatusIdle, Seq: 1}
	_, ok := c.Ready(context.Background(), "w9Z:p1M", baseline, time.Hour)
	if ok {
		t.Fatal("want false")
	}
	if s.pos != 3 {
		t.Errorf("want exactly 3 gets, got %d", s.pos)
	}
	if sleeps > 2 {
		t.Errorf("want ≤2 sleeps, got %d", sleeps)
	}
}

func TestReady_SoftUnknown_WaitsForWindow(t *testing.T) {
	softErr := scriptedCall{out: notFoundJSON, err: fmt.Errorf("exit status 1")}
	s := newScripter(softErr, softErr, softErr, softErr, softErr, scriptedCall{out: idleJSON6})
	c := herdragent.New(s.Run,
		herdragent.WithSleep(noop),
		herdragent.WithSettle(1500*time.Millisecond),
		herdragent.WithUnknownWindow(15*time.Second),
	)
	baseline := herdragent.State{Status: herdragent.StatusIdle, Seq: 5}
	st, ok := c.Ready(context.Background(), "w9Z:p1M", baseline, time.Hour)
	if !ok {
		t.Fatalf("want ok=true when idle appears within window, got state %+v", st)
	}
	if st.Seq != 6 {
		t.Errorf("want seq=6, got %d", st.Seq)
	}
}

func TestReady_SoftUnknown_ExitsAfterWindow(t *testing.T) {
	sleeps := 0
	softErr := scriptedCall{out: notFoundJSON, err: fmt.Errorf("exit status 1")}
	calls := make([]scriptedCall, 20)
	for i := range calls {
		calls[i] = softErr
	}
	s := newScripter(calls...)
	c := herdragent.New(s.Run,
		herdragent.WithSleep(func(_ context.Context, _ time.Duration) error {
			sleeps++
			return nil
		}),
		herdragent.WithSettle(1500*time.Millisecond),
		herdragent.WithUnknownWindow(15*time.Second),
	)
	baseline := herdragent.State{Status: herdragent.StatusIdle, Seq: 1}
	_, ok := c.Ready(context.Background(), "w9Z:p1M", baseline, time.Hour)
	if ok {
		t.Fatal("want false after window expires")
	}
	if s.pos <= 3 {
		t.Errorf("want more than 3 gets (soft should not exit at 3), got %d", s.pos)
	}
	if sleeps > 10 {
		t.Errorf("want ≤10 sleeps (window/settle=15/1.5=10), got %d", sleeps)
	}
}

func TestState_FieldSet(t *testing.T) {
	var s herdragent.State
	typ := reflect.TypeOf(s)
	names := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		names[typ.Field(i).Name] = true
	}
	banned := []string{"Complete", "Completion", "Done", "Finished", "IsComplete", "IsFinished"}
	for _, b := range banned {
		if names[b] {
			t.Errorf("State must not expose completion field %q (AC-4)", b)
		}
	}
	want := []string{"Status", "Seq", "Agent", "PaneID", "Question", "Settled", "Reason"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("State missing expected field %q", w)
		}
	}
}

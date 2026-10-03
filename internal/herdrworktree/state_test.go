package herdrworktree

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseListBindingByRef(t *testing.T) {
	out := "workspace_id=w1\thandle=p/a\tsandbox_id=abc123\tpane_id=pn1\n"
	ws, h, sb, pane, ok := ParseListBindingByRef(out, "abc")
	if !ok || ws != "w1" || h != "p/a" || sb != "abc123" || pane != "pn1" {
		t.Fatalf("got %v %v %v %v %v", ws, h, sb, pane, ok)
	}
	if _, _, ok := ParseListBinding(out, "w2"); ok {
		t.Fatal("unexpected match")
	}
}

func TestSandboxListedAndNotFound(t *testing.T) {
	if !SandboxListed("p/a running\n", "p/a", "") || SandboxListed("other\n", "p/a", "id") {
		t.Fatal("SandboxListed wrong")
	}
	if !IsSandboxNotFound(errors.New("exit 1"), "Sandbox NOT FOUND") {
		t.Fatal("IsSandboxNotFound wrong")
	}
}

func TestTeardownUnboundFallsBackToSandboxRm(t *testing.T) {
	var calls []string
	r := Runners{Host: func(_ context.Context, argv ...string) (string, error) {
		calls = append(calls, strings.Join(argv, " "))
		return "ok", nil
	}}
	res, err := Teardown(context.Background(), "p/a", false, r)
	if err != nil || res.Bound || res.Output != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(calls) != 2 || calls[1] != "sandbox rm p/a" {
		t.Fatalf("calls=%v", calls)
	}
}

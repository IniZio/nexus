package portfwd

import (
	"context"
	"testing"
)

func makeMgr(resps []runResp) (*Manager, *[][]string) {
	var calls [][]string
	fw := &Forwarder{
		ControlPath: testSock,
		SSHHost:     testHost,
		Run:         seqRun(&calls, resps),
	}
	return NewManager(fw), &calls
}

func TestReconcileAppliesForward(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("want 2 calls (present+apply), got %d", len(*calls))
	}
	if (*calls)[0][0] != "ss" {
		t.Fatalf("call 0 must be ss, got %v", (*calls)[0])
	}
	found := false
	for i, a := range (*calls)[1] {
		if a == "-L" && i+1 < len((*calls)[1]) {
			if (*calls)[1][i+1] == "3000:127.0.0.1:3000" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("apply must use same-port -L spec; argv=%v", (*calls)[1])
	}
}

func TestReconcileNoDoubleApply(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	ls := []Listener{{Port: 3000, Sandbox: ref}}
	if err := mgr.Reconcile(context.Background(), ls); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), ls); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("second reconcile must not re-apply; total calls=%d", len(*calls))
	}
}

func TestReconcileCancelOnStopped(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
		{code: 0},
	})
	runRef := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	stopRef := SandboxRef{ID: "abc", Status: SandboxStatusStopped}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: runRef}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: stopRef}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("want 3 calls (present+apply+cancel), got %d", len(*calls))
	}
	if len((*calls)[2]) < 3 || (*calls)[2][1] != "-O" || (*calls)[2][2] != "cancel" {
		t.Fatalf("call 3 must be ssh -O cancel, got %v", (*calls)[2])
	}
}

func TestReconcileCancelOnVanished(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("want 3 calls (present+apply+cancel), got %d", len(*calls))
	}
	if len((*calls)[2]) < 3 || (*calls)[2][1] != "-O" || (*calls)[2][2] != "cancel" {
		t.Fatalf("call 3 must be ssh -O cancel, got %v", (*calls)[2])
	}
}

func TestReconcileCancelOnPortGone(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
		{code: 0},
		{code: 0},
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 4000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, argv := range *calls {
		if len(argv) >= 3 && argv[1] == "-O" && argv[2] == "cancel" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ssh -O cancel found in calls=%v", *calls)
	}
}

func TestTeardownSandbox(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.TeardownSandbox(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("want 3 calls after teardown, got %d", len(*calls))
	}
	if (*calls)[2][2] != "cancel" {
		t.Fatalf("teardown call must be ssh -O cancel, got %v", (*calls)[2])
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("post-teardown empty reconcile must not add calls; got %d total", len(*calls))
	}
}

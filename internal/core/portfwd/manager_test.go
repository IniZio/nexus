package portfwd

import (
	"context"
	"net"
	"testing"
)

func noopListen(_, _ string) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func makeMgr(resps []runResp) (*Manager, *[][]string) {
	var calls [][]string
	fw := &Forwarder{
		ControlPath: testSock,
		SSHHost:     testHost,
		Run:         seqRun(&calls, resps),
		ListenFunc:  noopListen,
	}
	return NewManager(fw), &calls
}

func TestReconcileAppliesForward(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 call (present), got %d", len(*calls))
	}
	if (*calls)[0][0] != "ss" {
		t.Fatalf("call 0 must be ss, got %v", (*calls)[0])
	}
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].SandboxID != "abc" || entries[0].Port != 3000 {
		t.Fatalf("want [{abc 3000}] in Applied(), got %v", entries)
	}
}

func TestReconcileNoDoubleApply(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
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
	if len(*calls) != 1 {
		t.Fatalf("second reconcile must not re-apply; total calls=%d", len(*calls))
	}
}

func TestReconcileCancelOnStopped(t *testing.T) {
	mgr, calls := makeMgr([]runResp{{code: 0}})
	runRef := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	stopRef := SandboxRef{ID: "abc", Status: SandboxStatusStopped}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: runRef}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: stopRef}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 call (present only; local cancel uses lf.Close, not ssh), got %d: %v", len(*calls), *calls)
	}
	if len(mgr.Applied()) != 0 {
		t.Fatalf("want empty Applied() after stop-cancel, got %v", mgr.Applied())
	}
}

func TestReconcileCancelOnVanished(t *testing.T) {
	mgr, calls := makeMgr([]runResp{{code: 0}})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 call (present only), got %d: %v", len(*calls), *calls)
	}
	if len(mgr.Applied()) != 0 {
		t.Fatalf("want empty Applied() after vanish-cancel, got %v", mgr.Applied())
	}
}

func TestReconcileCancelOnPortGone(t *testing.T) {
	mgr, _ := makeMgr([]runResp{
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
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].Port != 4000 {
		t.Fatalf("want only port 4000 in Applied(); got %v", entries)
	}
}

func TestReconcileReappliesOnHostPortChange(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{code: 0},
		{code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, HostPort: 41234, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, HostPort: 41235, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].Port != 3000 {
		t.Fatalf("want port 3000 still applied after host port change; got %v", entries)
	}
	if len(*calls) != 2 {
		t.Fatalf("want 2 Present calls (one per apply); got %d: %v", len(*calls), *calls)
	}
}

func TestTeardownSandbox(t *testing.T) {
	mgr, calls := makeMgr([]runResp{{code: 0}})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.TeardownSandbox(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Applied()) != 0 {
		t.Fatalf("want empty Applied() after teardown, got %v", mgr.Applied())
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 call total (only the initial Present); got %d", len(*calls))
	}
}

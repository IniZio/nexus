package portfwd

import (
	"context"
	"testing"
)

// Mutation-pin: removing the Cancel block in Manager.Reconcile makes this fail.
func TestReconcileCancel2PortsTo1(t *testing.T) {
	mgr, _ := makeMgr([]runResp{
		{code: 0}, // Present(5001) → absent
		{code: 0}, // Present(5002) → absent
	})
	ref := SandboxRef{ID: "sb1", Status: SandboxStatusRunning}

	err := mgr.Reconcile(context.Background(), []Listener{
		{Port: 5001, Sandbox: ref},
		{Port: 5002, Sandbox: ref},
	})
	if err != nil {
		t.Fatalf("reconcile(2 ports): %v", err)
	}

	err = mgr.Reconcile(context.Background(), []Listener{
		{Port: 5001, Sandbox: ref},
	})
	if err != nil {
		t.Fatalf("reconcile(1 port): %v", err)
	}

	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].Port != 5001 {
		t.Fatalf("expected only port 5001 in Applied() after drop; got %v", entries)
	}
}

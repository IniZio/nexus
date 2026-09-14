package portfwd

import (
	"context"
	"testing"
)

// TestReconcileCancel2PortsTo1 verifies that when the desired set shrinks from
// two ports to one, the removed port receives an ssh -O cancel call.
// Mutation-pin: removing the Cancel block in Manager.Reconcile makes this fail.
func TestReconcileCancel2PortsTo1(t *testing.T) {
	// 5 responses needed:
	//   present(5001) → not present, apply(5001), present(5002) → not present, apply(5002),
	//   cancel(5002) on second reconcile
	mgr, calls := makeMgr([]runResp{
		{code: 0}, // ss -ltn for Present(5001) → empty, not present
		{code: 0}, // ssh -O forward for Apply(5001)
		{code: 0}, // ss -ltn for Present(5002) → empty, not present
		{code: 0}, // ssh -O forward for Apply(5002)
		{code: 0}, // ssh -O cancel for Cancel(5002)
	})
	ref := SandboxRef{ID: "sb1", Status: SandboxStatusRunning}

	// First reconcile: 2 ports
	err := mgr.Reconcile(context.Background(), []Listener{
		{Port: 5001, Sandbox: ref},
		{Port: 5002, Sandbox: ref},
	})
	if err != nil {
		t.Fatalf("reconcile(2 ports): %v", err)
	}

	// Second reconcile: 1 port — 5002 must be cancelled
	err = mgr.Reconcile(context.Background(), []Listener{
		{Port: 5001, Sandbox: ref},
	})
	if err != nil {
		t.Fatalf("reconcile(1 port): %v", err)
	}

	// Verify ssh -O cancel was issued for port 5002.
	foundCancel := false
	for _, argv := range *calls {
		if len(argv) >= 3 && argv[1] == "-O" && argv[2] == "cancel" {
			foundCancel = true
			break
		}
	}
	if !foundCancel {
		t.Fatalf("expected ssh -O cancel for dropped port; all calls: %v", *calls)
	}
}

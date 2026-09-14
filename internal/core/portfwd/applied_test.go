package portfwd

import (
	"context"
	"testing"
)

func TestPresentWiredBeforeApply(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{stdout: "tcp LISTEN 0 128 0.0.0.0:3000 0.0.0.0:*", code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 call (ss only), got %d", len(*calls))
	}
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].Port != 3000 {
		t.Fatalf("want Applied=[{abc 3000}], got %v", entries)
	}
}

func TestWorkspaceSwitchAbsence(t *testing.T) {
	mgr, _ := makeMgr([]runResp{
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
	if len(mgr.Applied()) != 0 {
		t.Fatalf("Applied must be empty after teardown, got %v", mgr.Applied())
	}
}

func TestPortVanishRemovedFromApplied(t *testing.T) {
	mgr, _ := makeMgr([]runResp{
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
	if len(mgr.Applied()) != 0 {
		t.Fatalf("Applied must be empty after nil reconcile, got %v", mgr.Applied())
	}
}

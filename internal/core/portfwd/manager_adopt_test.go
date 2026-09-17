package portfwd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	masterPID      = "40518"
	ssOwnedBy40518 = "State Listen Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" +
		"LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:((\"ssh\",pid=40518,fd=5))\n"
	ssOwnedBy99999 = "LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:((\"node\",pid=99999,fd=21))\n"
	checkStderr    = "Master running (pid=40518)\n"
)

func hasCall(calls [][]string, op string, port string) bool {
	for _, argv := range calls {
		if len(argv) >= 5 && argv[1] == "-O" && argv[2] == op && argv[3] == "-L" && argv[4] == port+":127.0.0.1:"+port {
			return true
		}
	}
	return false
}

// F10-AC1: a listening port whose owner pid is our ControlMaster's pid is a
// forward left behind by a previous client on the SAME master. It must be
// adopted into the applied set without re-issuing -L and without a conflict.
func TestReconcileAdoptsPortOwnedByOurMaster(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{stdout: ssOwnedBy40518, code: 0}, // ss -ltnp: 3000 bound by pid 40518
		{stderr: checkStderr, code: 0},    // ssh -O check: master pid 40518
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if hasCall(*calls, "forward", "3000") {
		t.Fatalf("adopted port must not be re-forwarded; calls=%v", *calls)
	}
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].SandboxID != "abc" || entries[0].Port != 3000 {
		t.Fatalf("port owned by our master must be adopted into Applied(); got %v (calls=%v)", entries, *calls)
	}
}

// F10-AC2: same port shape, owner pid differs from the master pid => foreign
// conflict, not applied (D-9 unchanged).
func TestReconcilePortOwnedByForeignPidIsConflict(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{stdout: ssOwnedBy99999, code: 0},
		{stderr: checkStderr, code: 0},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if hasCall(*calls, "forward", "3000") {
		t.Fatalf("foreign port must not be forwarded; calls=%v", *calls)
	}
	if entries := mgr.Applied(); len(entries) != 0 {
		t.Fatalf("foreign port must not be in Applied(); got %v", entries)
	}
}

// F10-AC3: an adopted forward that leaves the desired set is cancelled.
func TestReconcileCancelsAdoptedForwardWhenUndesired(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{stdout: ssOwnedBy40518, code: 0}, // Present(3000) -> ours
		{stderr: checkStderr, code: 0},    // master pid
		{code: 0},                         // cancel 3000
		{code: 0},                         // Present(4000) -> absent
		{code: 0},                         // forward 4000
	})
	refA := SandboxRef{ID: "A", Status: SandboxStatusRunning}
	refB := SandboxRef{ID: "B", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: refA}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 4000, Sandbox: refB}}); err != nil {
		t.Fatal(err)
	}
	if !hasCall(*calls, "cancel", "3000") {
		t.Fatalf("adopted 3000 must be cancelled when focus moves to B; calls=%v", *calls)
	}
	for _, e := range mgr.Applied() {
		if e.Port == 3000 {
			t.Fatalf("3000 must leave Applied() after cancel; got %v", mgr.Applied())
		}
	}
}

// Seed: at start, ports bound by our master that no applied.json entry covers
// are adopted (sandbox unknown) so they are cancellable; a desired row for the
// same port re-keys the entry instead of cancel+re-forward.
func TestAdoptMasterForwardsSeedsAppliedWithoutAppliedJSON(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.ctl")
	mgr, calls := makeMgrWithSock(sock, []runResp{
		{stderr: checkStderr, code: 0},    // ssh -O check: master pid
		{stdout: ssOwnedBy40518, code: 0}, // ss -ltnp: 3000 owned by master
		{code: 0},                         // cancel 3000 on reconcile(nil)
	})
	if err := mgr.AdoptMasterForwards(context.Background()); err != nil {
		t.Fatal(err)
	}
	if entries := mgr.Applied(); len(entries) != 1 || entries[0].Port != 3000 {
		t.Fatalf("seed must adopt 3000; got %v", entries)
	}
	raw, err := os.ReadFile(sock + ".applied.json")
	if err != nil {
		t.Fatalf("seed must persist applied.json: %v", err)
	}
	var pa persistedApplied
	if err := json.Unmarshal(raw, &pa); err != nil {
		t.Fatal(err)
	}
	if len(pa.Entries) != 1 || pa.Entries[0].Port != 3000 {
		t.Fatalf("applied.json must hold seeded 3000; got %v", pa.Entries)
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !hasCall(*calls, "cancel", "3000") {
		t.Fatalf("seeded stale forward must be cancelled; calls=%v", *calls)
	}
}

func TestReconcileRekeysSeededPortToDesiredSandbox(t *testing.T) {
	mgr, calls := makeMgr([]runResp{
		{stderr: checkStderr, code: 0},
		{stdout: ssOwnedBy40518, code: 0},
	})
	if err := mgr.AdoptMasterForwards(context.Background()); err != nil {
		t.Fatal(err)
	}
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if hasCall(*calls, "cancel", "3000") || hasCall(*calls, "forward", "3000") {
		t.Fatalf("re-key must not cancel or re-forward; calls=%v", *calls)
	}
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].SandboxID != "abc" || entries[0].Port != 3000 {
		t.Fatalf("want [{abc 3000}], got %v", entries)
	}
}

// macOS path: ss unavailable, netstat shows the listener, lsof names the owner.
func TestPresentMacOSOwnerViaLsof(t *testing.T) {
	macOut := "tcp4       0      0  127.0.0.1.3000         *.*                    LISTEN"
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{
		{err: os.ErrNotExist},          // ss
		{stdout: macOut, code: 0},      // netstat
		{stdout: "p40518\n", code: 0},  // lsof -Fp
		{stderr: checkStderr, code: 0}, // ssh -O check
	})}
	p, err := f.Present(context.Background(), 3000)
	if err != nil {
		t.Fatal(err)
	}
	if p != PresenceOurs {
		t.Fatalf("want PresenceOurs, got %v; calls=%v", p, calls)
	}
	wantLsof := []string{"lsof", "-nP", "-iTCP:3000", "-sTCP:LISTEN", "-Fp"}
	if len(calls) < 3 || !argvEq(calls[2], wantLsof) {
		t.Fatalf("lsof argv\n got  %v\n want %v", calls[2], wantLsof)
	}
}

func TestMasterForwardsMacOS(t *testing.T) {
	lsofOut := "p40518\nn127.0.0.1:3000\nn[::1]:3000\nn127.0.0.1:5173\n"
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{
		{stderr: checkStderr, code: 0}, // ssh -O check
		{err: os.ErrNotExist},          // ss
		{stdout: lsofOut, code: 0},     // lsof by pid
	})}
	ports, err := f.MasterForwards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 || ports[0] != 3000 || ports[1] != 5173 {
		t.Fatalf("want [3000 5173], got %v", ports)
	}
	wantLsof := []string{"lsof", "-nP", "-a", "-p", masterPID, "-iTCP", "-sTCP:LISTEN", "-Fn"}
	if !argvEq(calls[2], wantLsof) {
		t.Fatalf("lsof argv\n got  %v\n want %v", calls[2], wantLsof)
	}
}

func TestReconcileCancelFailureRetainsAppliedEntry(t *testing.T) {
	mgr, _ := makeMgr([]runResp{
		{code: 0},
		{code: 0},
		{code: 1, stderr: "cancel: no such forward"},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("cancel failure must not propagate as error: %v", err)
	}
	entries := mgr.Applied()
	if len(entries) != 1 || entries[0].Port != 3000 {
		t.Fatalf("cancel failure must retain Applied entry; got %v", entries)
	}
}

func TestReconcileCancelFailureWarnOnce(t *testing.T) {
	mgr, _ := makeMgr([]runResp{
		{code: 0},
		{code: 0},
		{code: 1, stderr: "fail"},
		{code: 1, stderr: "fail"},
	})
	ref := SandboxRef{ID: "abc", Status: SandboxStatusRunning}
	if err := mgr.Reconcile(context.Background(), []Listener{{Port: 3000, Sandbox: ref}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("first cancel failure: %v", err)
	}
	if err := mgr.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("second cancel failure: %v", err)
	}
	entries := mgr.Applied()
	if len(entries) != 1 {
		t.Fatalf("entry must be retained after repeated cancel failure; got %v", entries)
	}
}

func TestMasterForwardsNoMaster(t *testing.T) {
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{code: 255}})}
	ports, err := f.MasterForwards(context.Background())
	if err != nil || len(ports) != 0 {
		t.Fatalf("dead master: want no ports, nil err; got %v, %v", ports, err)
	}
}

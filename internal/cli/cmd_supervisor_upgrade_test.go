package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// newSupervisorUpgradeTestSandbox creates a sandbox record in an isolated
// store and returns the service, sandbox, and its supervisor state dir.
// Mirrors newEgressTestSandbox in cmd_egress_test.go.
func newSupervisorUpgradeTestSandbox(t *testing.T) (*service.Service, domain.Sandbox, string) {
	t.Helper()
	// AF_UNIX sun_path is capped at 107 bytes. t.TempDir() embeds the full
	// (sub)test name — e.g.
	// ".../TestSupervisorUpgrade_PartialNetnsIdentity_EachFieldAloneRefuses/missing_only_CHAPISocket/001" —
	// which, joined with "supervisors/<sandboxID>/supervisor.sock", blows the
	// limit and net.Listen("unix", ...) fails with "bind: invalid argument"
	// before the test ever reaches an assertion. A short, name-independent
	// dir under /tmp keeps the eventual socket path well under the cap.
	stateRoot, err := os.MkdirTemp("/tmp", "n3")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(stateRoot) })
	t.Setenv("XDG_STATE_HOME", stateRoot)

	svc, err := newSandboxService()
	if err != nil {
		t.Fatalf("newSandboxService: %v", err)
	}
	sb, err := svc.Create(context.Background(), "proj", "upgrade-cmd", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	stateDir := filepath.Join(storeRoot, "supervisors", sb.ID.String())
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll stateDir: %v", err)
	}
	return svc, sb, stateDir
}

// listenFakeSupervisorSock starts a bare Unix listener at stateDir/supervisor.sock
// so supervisorSockLooksAlive's dial check succeeds, without a real supervisor
// process behind it. Returns the socket path; the listener is closed on test
// cleanup.
func listenFakeSupervisorSock(t *testing.T, stateDir string) string {
	t.Helper()
	sockPath := filepath.Join(stateDir, "supervisor.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen fake supervisor sock: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return sockPath
}

// markRunningWithLiveSupervisor forces the sandbox record into Running with
// SupervisorPID set to this test process's own pid (always alive) and
// SupervisorSock pointing at a fake listener, bypassing the lifecycle
// machine directly via store.Update — the same pattern
// TestEgressAllow_PersistsHost uses to inspect persisted state.
func markRunningWithLiveSupervisor(t *testing.T, sb domain.Sandbox, sockPath string) {
	t.Helper()
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("store.DefaultRoot: %v", err)
	}
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Running
		rec.SupervisorPID = os.Getpid()
		rec.SupervisorSock = sockPath
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
}

// listenFakeSupervisorHTTP starts a real HTTP server (like the production
// supervisor's IPC mux) over a Unix socket at stateDir/supervisor.sock,
// serving GET /supervisor/version with versionHash and GET
// /supervisor/agent-health with the given state. Used to drive
// runSupervisorUpgradeWith's health-aware noop check through its REAL client
// call (supervisor.RequestSupervisorVersion / supervisor.RequestAgentHealth)
// against a REAL server, not a hand-rolled stand-in for the check itself.
func listenFakeSupervisorHTTP(t *testing.T, stateDir, versionHash, healthState string) string {
	t.Helper()
	sockPath := filepath.Join(stateDir, "supervisor.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen fake supervisor http sock: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/supervisor/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"binary_hash":%q}`, versionHash)
	})
	mux.HandleFunc("/supervisor/agent-health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if healthState == string(supervisor.AgentChannelHealthy) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusOK) // handler always 200s a well-formed verdict
		}
		fmt.Fprintf(w, `{"state":%q,"control_err":"fake probe"}`, healthState)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sockPath
}

// setCompleteNetnsIdentity fills every field runSupervisorUpgradeWith's
// incomplete-identity guard requires, so tests below reach the noop/health
// check rather than refusing earlier for an unrelated reason.
func setCompleteNetnsIdentity(t *testing.T, sb domain.Sandbox) {
	t.Helper()
	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.NetnsChildPID = 4242
		rec.NetnsChildPGID = 4242
		rec.NetnsChildStartTime = 123456
		rec.GuestTapName = "nx3g-test"
		rec.CHAPISocket = "/tmp/fake.sock"
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
}

// TestSupervisorUpgrade_SameBinaryHealthy_Noop proves the true no-op case:
// same binary hash AND a live, healthy agent-health probe together refuse.
func TestSupervisorUpgrade_SameBinaryHealthy_Noop(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	myHash, err := supervisor.HashOwnBinary()
	if err != nil {
		t.Fatalf("HashOwnBinary: %v", err)
	}
	sockPath := listenFakeSupervisorHTTP(t, stateDir, myHash, string(supervisor.AgentChannelHealthy))
	markRunningWithLiveSupervisor(t, sb, sockPath)
	setCompleteNetnsIdentity(t, sb)

	out, _, _ := capture(false)
	err = runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, out, svc)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != supervisorUpgradeNoopCode {
		t.Fatalf("expected %q, got %v", supervisorUpgradeNoopCode, err)
	}
}

// TestSupervisorUpgrade_SameBinaryButChannelDown_ProceedsPastNoop is the
// mutation-bearing proof for this slice's fix to secondary defect #1: a
// supervisor reporting the CURRENT binary hash, whose agent-health probe
// reports down_guest_alive, must NOT be treated as a no-op — the caller
// needs a way to force a re-adopt of a wedged-but-same-binary supervisor.
// It proceeds all the way to the next real refusal (no persisted spawn
// spec), proving the noop code path was never taken.
func TestSupervisorUpgrade_SameBinaryButChannelDown_ProceedsPastNoop(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	myHash, err := supervisor.HashOwnBinary()
	if err != nil {
		t.Fatalf("HashOwnBinary: %v", err)
	}
	sockPath := listenFakeSupervisorHTTP(t, stateDir, myHash, string(supervisor.AgentChannelDownGuestAlive))
	markRunningWithLiveSupervisor(t, sb, sockPath)
	setCompleteNetnsIdentity(t, sb)

	out, _, _ := capture(false)
	err = runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, out, svc)
	ce, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if ce.Code == supervisorUpgradeNoopCode {
		t.Fatalf("got noop refusal despite an unhealthy agent channel — the health-aware check did not run")
	}
	if ce.Code != supervisorUpgradeNoSpawnSpecCode {
		t.Fatalf("expected to proceed to %q, got %q (%v)", supervisorUpgradeNoSpawnSpecCode, ce.Code, err)
	}
}

// TestSupervisorUpgrade_Force_SkipsNoopEvenWhenHealthy proves the explicit
// --force escape hatch bypasses BOTH the binary-hash check and the
// health-aware check, without even asking the fake server for agent-health
// (the server here reports Healthy — if the health check ran, it would
// refuse; force must prevent that check from running at all).
func TestSupervisorUpgrade_Force_SkipsNoopEvenWhenHealthy(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	myHash, err := supervisor.HashOwnBinary()
	if err != nil {
		t.Fatalf("HashOwnBinary: %v", err)
	}
	sockPath := listenFakeSupervisorHTTP(t, stateDir, myHash, string(supervisor.AgentChannelHealthy))
	markRunningWithLiveSupervisor(t, sb, sockPath)
	setCompleteNetnsIdentity(t, sb)

	out, _, _ := capture(false)
	err = runSupervisorUpgradeWith(context.Background(), sb.Handle(), true, out, svc)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != supervisorUpgradeNoSpawnSpecCode {
		t.Fatalf("expected --force to proceed to %q, got %v", supervisorUpgradeNoSpawnSpecCode, err)
	}
}

// TestSupervisorUpgrade_NotRunning_Refuses proves the state guard: a freshly
// created (not-running) sandbox is refused before anything else is checked,
// and the store record is untouched.
func TestSupervisorUpgrade_NotRunning_Refuses(t *testing.T) {
	svc, sb, _ := newSupervisorUpgradeTestSandbox(t)

	out, _, _ := capture(false)
	err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, out, svc)
	if err == nil {
		t.Fatal("expected error for a non-running sandbox")
	}
	ce, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if ce.Code != supervisorUpgradeNotRunningCode {
		t.Errorf("wrong code: got %q, want %q", ce.Code, supervisorUpgradeNotRunningCode)
	}

	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	rec, _ := st.Get(context.Background(), sb.ID)
	if rec.SupervisorPID != 0 || rec.SupervisorSock != "" {
		t.Errorf("store was mutated despite refusal: %+v", rec)
	}
}

// TestSupervisorUpgrade_NoSupervisor_Refuses proves the supervisor-liveness
// guard: a Running sandbox with no live supervisor socket is refused, using
// the same shape as TestEgressAllow_NoSupervisor_ReturnsError.
func TestSupervisorUpgrade_NoSupervisor_Refuses(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)

	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Running
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
	_ = stateDir // no socket file created: supervisor looks absent

	out, _, _ := capture(false)
	err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, out, svc)
	if err == nil {
		t.Fatal("expected error for a sandbox with no live supervisor")
	}
	ce, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if ce.Code != supervisorUpgradeNoSupervisorCode {
		t.Errorf("wrong code: got %q, want %q", ce.Code, supervisorUpgradeNoSupervisorCode)
	}
}

// TestSupervisorUpgrade_IncompleteNetnsIdentity_Refuses is the mutation-bearing
// proof for this motive's most commonly broken rail: a Running sandbox with a
// live (dialable) supervisor socket but a zero/absent netns identity must
// refuse and must NOT reach the spawn step. Absence of any handoff-*.sock
// file under stateDir after the call proves SpawnAdoptDetached was never
// invoked — the strongest available "no mutation" signal short of process
// tracing.
func TestSupervisorUpgrade_IncompleteNetnsIdentity_Refuses(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	sockPath := listenFakeSupervisorSock(t, stateDir)
	markRunningWithLiveSupervisor(t, sb, sockPath)
	// Deliberately leave NetnsChildPID/PGID/StartTime/GuestTapName/CHAPISocket
	// at their zero values — this is the incomplete-identity case.

	out, _, _ := capture(false)
	err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, out, svc)
	if err == nil {
		t.Fatal("expected error for a sandbox with an incomplete netns identity")
	}
	ce, ok := err.(*CodedError)
	if !ok {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if ce.Code != supervisorUpgradeIncompleteNetnsCode {
		t.Errorf("wrong code: got %q, want %q", ce.Code, supervisorUpgradeIncompleteNetnsCode)
	}

	entries, readErr := os.ReadDir(stateDir)
	if readErr != nil {
		t.Fatalf("ReadDir stateDir: %v", readErr)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sock" && e.Name() != "supervisor.sock" {
			t.Errorf("found unexpected socket %q — a replacement supervisor was spawned despite the incomplete-identity refusal", e.Name())
		}
	}

	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	rec, _ := st.Get(context.Background(), sb.ID)
	if rec.SupervisorPID != os.Getpid() {
		t.Errorf("SupervisorPID changed despite refusal: got %d, want %d (unchanged)", rec.SupervisorPID, os.Getpid())
	}
}

// TestSupervisorUpgrade_PartialNetnsIdentity_EachFieldAloneRefuses proves the
// guard checks all five fields independently, not just "all zero vs all
// set". Two families of row, per field:
//
//   - "only X set": every other field is zero. This refuses trivially — the
//     other four zero fields are enough to trip the guard on their own — so
//     it exercises the compound condition, not the individual conjunct for
//     X. Cheap to keep (catches wholesale guard deletion), but NOT
//     sufficient on its own: disabling any single conjunct except the last
//     one in source order still passes every "only X set" row, because the
//     other conjuncts are still zero and still refuse.
//   - "missing only X": every field EXCEPT X is populated with a valid
//     value; X alone is zero/empty. This is the row that actually pins
//     conjunct X — if X's check is deleted, this is the only row that goes
//     GREEN incorrectly, because it is the only row where every other
//     conjunct is satisfied.
//
// A prior version of this table had only one "missing only X" row (for
// CHAPISocket) — an advisor gate found that disabling any of the other four
// conjuncts individually left every remaining row green, so four of five
// checks were asserted but never actually exercised.
func TestSupervisorUpgrade_PartialNetnsIdentity_EachFieldAloneRefuses(t *testing.T) {
	cases := []struct {
		name string
		set  func(rec *domain.Sandbox)
	}{
		{"only PID", func(rec *domain.Sandbox) { rec.NetnsChildPID = 4242 }},
		{"only PGID", func(rec *domain.Sandbox) { rec.NetnsChildPGID = 4242 }},
		{"only StartTime", func(rec *domain.Sandbox) { rec.NetnsChildStartTime = 123456 }},
		{"only GuestTapName", func(rec *domain.Sandbox) { rec.GuestTapName = "nx3g-test" }},
		{"only CHAPISocket", func(rec *domain.Sandbox) { rec.CHAPISocket = "/tmp/fake.sock" }},
		{"missing only PID", func(rec *domain.Sandbox) {
			rec.NetnsChildPGID = 4242
			rec.NetnsChildStartTime = 123456
			rec.GuestTapName = "nx3g-test"
			rec.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only PGID", func(rec *domain.Sandbox) {
			rec.NetnsChildPID = 4242
			rec.NetnsChildStartTime = 123456
			rec.GuestTapName = "nx3g-test"
			rec.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only StartTime", func(rec *domain.Sandbox) {
			rec.NetnsChildPID = 4242
			rec.NetnsChildPGID = 4242
			rec.GuestTapName = "nx3g-test"
			rec.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only GuestTapName", func(rec *domain.Sandbox) {
			rec.NetnsChildPID = 4242
			rec.NetnsChildPGID = 4242
			rec.NetnsChildStartTime = 123456
			rec.CHAPISocket = "/tmp/fake.sock"
		}},
		{"missing only CHAPISocket", func(rec *domain.Sandbox) {
			rec.NetnsChildPID = 4242
			rec.NetnsChildPGID = 4242
			rec.NetnsChildStartTime = 123456
			rec.GuestTapName = "nx3g-test"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
			sockPath := listenFakeSupervisorSock(t, stateDir)
			markRunningWithLiveSupervisor(t, sb, sockPath)

			storeRoot, _ := store.DefaultRoot()
			st, _ := store.NewFileStore(storeRoot)
			if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
				tc.set(rec)
				return nil
			}); err != nil {
				t.Fatalf("st.Update: %v", err)
			}

			out, _, _ := capture(false)
			err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, out, svc)
			ce, ok := err.(*CodedError)
			if !ok || ce.Code != supervisorUpgradeIncompleteNetnsCode {
				t.Fatalf("case %q: expected %q, got %v", tc.name, supervisorUpgradeIncompleteNetnsCode, err)
			}
		})
	}
}

package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newSupervisorUpgradeTestSandbox creates a sandbox record in an isolated
// store and returns the service, sandbox, and its supervisor state dir.
// Mirrors newEgressTestSandbox in cmd_egress_test.go.
func newSupervisorUpgradeTestSandbox(t *testing.T) (*service.Service, domain.Sandbox, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

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

// TestSupervisorUpgrade_NotRunning_Refuses proves the state guard: a freshly
// created (not-running) sandbox is refused before anything else is checked,
// and the store record is untouched.
func TestSupervisorUpgrade_NotRunning_Refuses(t *testing.T) {
	svc, sb, _ := newSupervisorUpgradeTestSandbox(t)

	out, _, _ := capture(false)
	err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), out, svc)
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
	err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), out, svc)
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
	err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), out, svc)
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
// set" — a record with four of the five fields populated must still refuse.
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
			err := runSupervisorUpgradeWith(context.Background(), sb.Handle(), out, svc)
			ce, ok := err.(*CodedError)
			if !ok || ce.Code != supervisorUpgradeIncompleteNetnsCode {
				t.Fatalf("case %q: expected %q, got %v", tc.name, supervisorUpgradeIncompleteNetnsCode, err)
			}
		})
	}
}

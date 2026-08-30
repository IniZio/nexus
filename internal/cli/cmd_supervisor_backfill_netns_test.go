package cli

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// writeFakeSpawnSpec persists a minimal spawn.json under stateDir with
// SocketDir set, so ReadSpawnSpec (and this verb's expected-API-socket
// computation) succeed. Mirrors what WriteSpawnSpec does on a real boot.
func writeFakeSpawnSpec(t *testing.T, stateDir, socketDir string) {
	t.Helper()
	if err := supervisor.WriteSpawnSpec(stateDir, supervisor.Config{
		SandboxRef: "irrelevant",
		StoreRoot:  "irrelevant",
		SocketDir:  socketDir,
	}); err != nil {
		t.Fatalf("WriteSpawnSpec: %v", err)
	}
}

// spawnFakeNetnsChildForCLI starts a "sleep" child of THIS test process with
// an environ shaped like a real netns child's, for exercising
// supervisor-backfill-netns-identity end to end without a real VM.
func spawnFakeNetnsChildForCLI(t *testing.T, apiSocket, tap string) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.Env = []string{
		cloudhypervisor.NetnsRunEnv + "=1",
		cloudhypervisor.NetnsEnvAPISocket + "=" + apiSocket,
		cloudhypervisor.NetnsEnvGuestTap + "=" + tap,
	}
	// Real netns children always set Setpgid:true (netnsChildAttr,
	// ch_netns.go) so pgid == pid; mirror that here so this fake candidate
	// has the same shape BackfillNetnsIdentity will see in production.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake netns child: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return pid
}

// TestBackfillNetnsIdentityCmd_NotRunning_Refuses proves the state guard
// runs first, same shape as TestSupervisorUpgrade_NotRunning_Refuses.
func TestBackfillNetnsIdentityCmd_NotRunning_Refuses(t *testing.T) {
	svc, sb, _ := newSupervisorUpgradeTestSandbox(t)

	out, _, _ := capture(false)
	err := runSupervisorBackfillNetnsIdentityWith(context.Background(), sb.Handle(), out, svc)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != backfillNetnsNotRunningCode {
		t.Fatalf("expected %q, got %v", backfillNetnsNotRunningCode, err)
	}
}

// TestBackfillNetnsIdentityCmd_AlreadyPresent_Refuses proves this verb
// refuses to silently overwrite a sandbox that already has a complete netns
// identity.
func TestBackfillNetnsIdentityCmd_AlreadyPresent_Refuses(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	sockPath := listenFakeSupervisorSock(t, stateDir)
	markRunningWithLiveSupervisor(t, sb, sockPath)

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

	out, _, _ := capture(false)
	err := runSupervisorBackfillNetnsIdentityWith(context.Background(), sb.Handle(), out, svc)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != backfillNetnsAlreadyPresentCode {
		t.Fatalf("expected %q, got %v", backfillNetnsAlreadyPresentCode, err)
	}
}

// TestBackfillNetnsIdentityCmd_NoSupervisor_Refuses mirrors
// TestSupervisorUpgrade_NoSupervisor_Refuses.
func TestBackfillNetnsIdentityCmd_NoSupervisor_Refuses(t *testing.T) {
	svc, sb, _ := newSupervisorUpgradeTestSandbox(t)

	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Running
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}

	out, _, _ := capture(false)
	err := runSupervisorBackfillNetnsIdentityWith(context.Background(), sb.Handle(), out, svc)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != backfillNetnsNoSupervisorCode {
		t.Fatalf("expected %q, got %v", backfillNetnsNoSupervisorCode, err)
	}
}

// TestBackfillNetnsIdentityCmd_NoZeroCandidates_Refuses proves that with a
// live supervisor and a spawn spec, but no matching netns-shaped child, the
// verb refuses via BackfillNetnsIdentity's own zero-candidate rail and does
// NOT persist anything.
func TestBackfillNetnsIdentityCmd_NoZeroCandidates_Refuses(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	sockPath := listenFakeSupervisorSock(t, stateDir)
	markRunningWithLiveSupervisor(t, sb, sockPath)

	socketDir := t.TempDir()
	writeFakeSpawnSpec(t, stateDir, socketDir)
	// No fake netns child spawned: zero candidates under this test process.

	out, _, _ := capture(false)
	err := runSupervisorBackfillNetnsIdentityWith(context.Background(), sb.Handle(), out, svc)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != backfillNetnsReconstructFailedCode {
		t.Fatalf("expected %q, got %v", backfillNetnsReconstructFailedCode, err)
	}

	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	rec, _ := st.Get(context.Background(), sb.ID)
	if rec.NetnsChildPID != 0 || rec.CHAPISocket != "" {
		t.Errorf("store was mutated despite refusal: %+v", rec)
	}
}

// TestBackfillNetnsIdentityCmd_HappyPath_PersistsIdentity is the end-to-end
// proof: a live supervisor with exactly one matching netns-shaped child
// yields a persisted identity with all five fields populated, matching the
// candidate's live /proc state.
func TestBackfillNetnsIdentityCmd_HappyPath_PersistsIdentity(t *testing.T) {
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	// The "live supervisor" in this test IS this test process (os.Getpid()),
	// exactly as markRunningWithLiveSupervisor already sets up — so a real
	// child of this process satisfies the ppid==SupervisorPID predicate.
	sockPath := listenFakeSupervisorSock(t, stateDir)
	markRunningWithLiveSupervisor(t, sb, sockPath)

	socketDir := t.TempDir()
	writeFakeSpawnSpec(t, stateDir, socketDir)
	expectedAPISocket := socketDir + "/" + sb.ID.String() + ".sock"
	pid := spawnFakeNetnsChildForCLI(t, expectedAPISocket, "nx3g-cli-test")
	// Give the kernel a moment to fully populate /proc for the new pid
	// before BackfillNetnsIdentity enumerates it (mirrors
	// netns_backfill_test.go's own settle delay).
	time.Sleep(20 * time.Millisecond)

	out, _, _ := capture(false)
	err := runSupervisorBackfillNetnsIdentityWith(context.Background(), sb.Handle(), out, svc)
	if err != nil {
		t.Fatalf("runSupervisorBackfillNetnsIdentityWith: %v", err)
	}

	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	rec, err := st.Get(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("st.Get: %v", err)
	}
	if rec.NetnsChildPID != pid {
		t.Errorf("NetnsChildPID = %d, want %d", rec.NetnsChildPID, pid)
	}
	if rec.NetnsChildPGID <= 0 {
		t.Errorf("NetnsChildPGID = %d, want positive", rec.NetnsChildPGID)
	}
	if rec.NetnsChildStartTime == 0 {
		t.Error("NetnsChildStartTime = 0, want nonzero")
	}
	if rec.GuestTapName != "nx3g-cli-test" {
		t.Errorf("GuestTapName = %q, want %q", rec.GuestTapName, "nx3g-cli-test")
	}
	if rec.CHAPISocket != expectedAPISocket {
		t.Errorf("CHAPISocket = %q, want %q", rec.CHAPISocket, expectedAPISocket)
	}

	live, err := cloudhypervisor.ReadProcStat(pid)
	if err != nil {
		t.Fatalf("ReadProcStat(%d): %v", pid, err)
	}
	if rec.NetnsChildStartTime != live.StartTime {
		t.Errorf("persisted starttime %d does not match live /proc starttime %d", rec.NetnsChildStartTime, live.StartTime)
	}
}

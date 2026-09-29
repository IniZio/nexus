package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/recovery"
	"github.com/IniZio/nexus/internal/core/store"
)

func withStubVirtiofsdScan(t *testing.T, procs []virtiofsdProc) {
	t.Helper()
	prev := scanVirtiofsd
	scanVirtiofsd = func(domain.SandboxID) []virtiofsdProc { return procs }
	t.Cleanup(func() { scanVirtiofsd = prev })
}

func setLiveMounts(t *testing.T, sb domain.Sandbox) {
	t.Helper()
	storeRoot, _ := store.DefaultRoot()
	st, _ := store.NewFileStore(storeRoot)
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.LiveMounts = []domain.LiveMount{{HostPath: "/tmp/x", GuestPath: "/mnt/x"}}
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
}

func upgradeWithLiveMounts(t *testing.T, procs []virtiofsdProc, forceDrop bool) error {
	t.Helper()
	svc, sb, stateDir := newSupervisorUpgradeTestSandbox(t)
	sockPath := listenFakeSupervisorSock(t, stateDir)
	markRunningWithLiveSupervisor(t, sb, sockPath)
	setCompleteNetnsIdentity(t, sb)
	setLiveMounts(t, sb)
	withStubVirtiofsdScan(t, procs)
	out, _, _ := capture(false)
	return runSupervisorUpgradeWith(context.Background(), sb.Handle(), false, forceDrop, out, svc)
}

func TestSupervisorUpgrade_LegacyVirtiofsd_Refuses(t *testing.T) {
	err := upgradeWithLiveMounts(t, []virtiofsdProc{{PID: 9001, PGID: 9001}}, false)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != supervisorUpgradeMountsWouldBreak {
		t.Fatalf("want %q, got %v", supervisorUpgradeMountsWouldBreak, err)
	}
	if !strings.Contains(ce.Msg, "sandbox stop") || !strings.Contains(ce.Msg, "sandbox start") {
		t.Errorf("message lacks stop/start remedy: %s", ce.Msg)
	}
}

func TestSupervisorUpgrade_LegacyVirtiofsd_ForceDropMountsProceeds(t *testing.T) {
	err := upgradeWithLiveMounts(t, []virtiofsdProc{{PID: 9001, PGID: 9001}}, true)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != supervisorUpgradeNoSpawnSpecCode {
		t.Fatalf("want to proceed to %q, got %v", supervisorUpgradeNoSpawnSpecCode, err)
	}
}

func TestSupervisorUpgrade_VirtiofsdInNetnsGroup_Proceeds(t *testing.T) {
	err := upgradeWithLiveMounts(t, []virtiofsdProc{{PID: 9001, PGID: 4242}}, false)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != supervisorUpgradeNoSpawnSpecCode {
		t.Fatalf("want to proceed to %q, got %v", supervisorUpgradeNoSpawnSpecCode, err)
	}
}

func recoverWithLiveMounts(t *testing.T, procs []virtiofsdProc) string {
	t.Helper()
	ctx := context.Background()
	st, drv, sb := newReacquirableTestFixture(t)
	if err := st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.LiveMounts = []domain.LiveMount{{HostPath: "/tmp/x", GuestPath: "/mnt/x"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	withStubAdoptSpawner(t, func(domain.Sandbox) (recovery.CAOutcome, error) { return recovery.CARecovered, nil })
	withStubVirtiofsdScan(t, procs)
	out, _, stderr := capture(false)
	if err := runRecoverWith(ctx, st, drv, out); err != nil {
		t.Fatalf("runRecoverWith: %v", err)
	}
	return stderr.String()
}

func TestRecover_LiveMountsLost_Warns(t *testing.T) {
	if got := recoverWithLiveMounts(t, nil); !strings.Contains(got, "live mounts lost; restart the sandbox (stop && start)") {
		t.Fatalf("no lost-mounts warning; stderr:\n%s", got)
	}
}

func TestRecover_LiveMountsIntact_NoWarning(t *testing.T) {
	if got := recoverWithLiveMounts(t, []virtiofsdProc{{PID: 9001, PGID: 4242}}); strings.Contains(got, "live mounts lost") {
		t.Fatalf("unexpected warning; stderr:\n%s", got)
	}
}


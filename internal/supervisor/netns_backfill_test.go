// netns_backfill_test.go — proves BackfillNetnsIdentity's identification
// predicate (ticket 11): exactly one live child of the supervisor pid, with
// NEXUS3_NETNS_RUN=1 and the expected API socket in its environ, wins;
// everything else refuses.
//
// These are hermetic: no CH binary, no /dev/kvm. A plain "sleep" process
// with a controlled Env stands in for a real netns child, exactly as
// ch_netns_adopt_test.go already does for AdoptNetnsRuntime.
package supervisor

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

// socketpairFile returns one end of an AF_UNIX SOCK_DGRAM socketpair as an
// *os.File, for tests that need a live perimFile to hand AdoptNetnsRuntime
// without exercising the real netns runtime plumbing.
func socketpairFile(t *testing.T) *os.File {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	other := os.NewFile(uintptr(fds[1]), "backfill-test-other-end")
	t.Cleanup(func() { other.Close() })
	return os.NewFile(uintptr(fds[0]), "backfill-test-perim")
}

// spawnFakeNetnsChild starts a "sleep" process that is a real child of THIS
// test process (so ppid == os.Getpid()), with an environ shaped like a real
// netns child's: NEXUS3_NETNS_RUN=1, NEXUS3_NETNS_API_SOCKET=apiSocket,
// NEXUS3_NETNS_GUEST_TAP=tap. Returns the pid; the process is killed and
// reaped on test cleanup.
func spawnFakeNetnsChild(t *testing.T, apiSocket, tap string) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.Env = []string{
		cloudhypervisor.NetnsRunEnv + "=1",
		cloudhypervisor.NetnsEnvAPISocket + "=" + apiSocket,
		cloudhypervisor.NetnsEnvGuestTap + "=" + tap,
	}
	// Real netns children always set Setpgid:true (netnsChildAttr,
	// ch_netns.go) so pgid == pid; mirror that here.
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

// spawnPlainChild starts a "sleep" process with no netns-shaped environ —
// stands in for an unrelated child of the same supervisor (e.g. a helper
// process) that must NOT be mistaken for the netns child.
func spawnPlainChild(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plain child: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return pid
}

// TestBackfillNetnsIdentity_HappyPath proves the normal case: one live child
// of the caller's own pid, correctly shaped, yields a verified identity
// whose starttime/pgid come from /proc and whose tap/socket come from the
// candidate's own environ.
func TestBackfillNetnsIdentity_HappyPath(t *testing.T) {
	const wantSocket = "/tmp/nx3-backfill-test.sock"
	const wantTap = "nx3g-backfill"
	pid := spawnFakeNetnsChild(t, wantSocket, wantTap)

	// Give the kernel a moment to fully populate /proc for the new pid.
	time.Sleep(20 * time.Millisecond)

	id, err := BackfillNetnsIdentity(os.Getpid(), wantSocket)
	if err != nil {
		t.Fatalf("BackfillNetnsIdentity: %v", err)
	}
	if id.ChildPID != pid {
		t.Errorf("ChildPID = %d, want %d", id.ChildPID, pid)
	}
	if id.ChildPGID <= 0 {
		t.Errorf("ChildPGID = %d, want positive", id.ChildPGID)
	}
	if id.ChildStartTime == 0 {
		t.Error("ChildStartTime = 0, want nonzero")
	}
	if id.GuestTapName != wantTap {
		t.Errorf("GuestTapName = %q, want %q", id.GuestTapName, wantTap)
	}
	if id.APISocket != wantSocket {
		t.Errorf("APISocket = %q, want %q", id.APISocket, wantSocket)
	}

	// The starttime persisted must actually match what /proc reports right
	// now — this is the "read starttime after settling on the pid, from the
	// same read" ordering requirement. A stale or fabricated value would
	// still pass the fields-nonzero checks above but fail this comparison.
	live, err := cloudhypervisor.ReadProcStat(pid)
	if err != nil {
		t.Fatalf("ReadProcStat(%d): %v", pid, err)
	}
	if id.ChildStartTime != live.StartTime {
		t.Errorf("ChildStartTime = %d does not match live /proc starttime %d", id.ChildStartTime, live.StartTime)
	}
	if id.ChildPGID != live.PGID {
		t.Errorf("ChildPGID = %d does not match live /proc pgid %d", id.ChildPGID, live.PGID)
	}
}

// TestBackfillNetnsIdentity_ZeroCandidates_Refuses proves the zero-candidate
// refusal: a live supervisor pid with no netns-shaped child at all must
// refuse, not fabricate an identity.
func TestBackfillNetnsIdentity_ZeroCandidates_Refuses(t *testing.T) {
	// A plain child exists, but it is not netns-shaped, so it must not count.
	spawnPlainChild(t)
	time.Sleep(20 * time.Millisecond)

	_, err := BackfillNetnsIdentity(os.Getpid(), "/tmp/nx3-nonexistent.sock")
	if err == nil {
		t.Fatal("expected refusal for zero matching candidates, got nil")
	}
}

// TestBackfillNetnsIdentity_MultiCandidate_Refuses proves the
// multi-candidate refusal: TWO children both shaped like a netns child for
// the SAME expected socket must refuse rather than pick-the-first. This is
// an intentionally contrived scenario (two real netns children would never
// share one API socket in production) but it is exactly the shape the
// identification predicate must not silently resolve by picking one.
func TestBackfillNetnsIdentity_MultiCandidate_Refuses(t *testing.T) {
	const wantSocket = "/tmp/nx3-backfill-multi.sock"
	spawnFakeNetnsChild(t, wantSocket, "nx3g-a")
	spawnFakeNetnsChild(t, wantSocket, "nx3g-b")
	time.Sleep(20 * time.Millisecond)

	_, err := BackfillNetnsIdentity(os.Getpid(), wantSocket)
	if err == nil {
		t.Fatal("expected refusal for multiple matching candidates, got nil")
	}
}

// TestBackfillNetnsIdentity_WrongAPISocket_Refuses proves the socket-binding
// predicate: a real netns-shaped child of the right supervisor, but carrying
// a DIFFERENT sandbox's API socket, must not be adopted for this sandbox.
func TestBackfillNetnsIdentity_WrongAPISocket_Refuses(t *testing.T) {
	spawnFakeNetnsChild(t, "/tmp/nx3-other-sandbox.sock", "nx3g-other")
	time.Sleep(20 * time.Millisecond)

	_, err := BackfillNetnsIdentity(os.Getpid(), "/tmp/nx3-this-sandbox.sock")
	if err == nil {
		t.Fatal("expected refusal when the only candidate's API socket belongs to a different sandbox, got nil")
	}
}

// TestBackfillNetnsIdentity_RejectsInvalidArgs pins the argument-shape guard
// (mirrors AdoptNetnsRuntime's own non-positive-pid rejection).
func TestBackfillNetnsIdentity_RejectsInvalidArgs(t *testing.T) {
	if _, err := BackfillNetnsIdentity(0, "/tmp/nx3-x.sock"); err == nil {
		t.Error("expected refusal for supervisorPID=0, got nil")
	}
	if _, err := BackfillNetnsIdentity(os.Getpid(), ""); err == nil {
		t.Error("expected refusal for empty expectedAPISocket, got nil")
	}
}

// TestBackfillNetnsIdentity_ThenAdopt_StarttimeMismatch_Refuses is the
// ticket's acceptance gate: mutation-prove that a BACKFILLED identity still
// refuses AdoptNetnsRuntime on a starttime mismatch. It is not enough that
// backfill populates fields — a record whose backfilled starttime does not
// match /proc must still hit the pid-reuse guard, exactly as a
// start-of-day-persisted identity would.
//
// This is done WITHOUT actually recycling a pid (which the test cannot
// reliably force): it takes a real, successfully backfilled identity and
// then corrupts ONLY the persisted starttime before handing it to
// AdoptNetnsRuntime — the same shape a real pid-reuse event would present to
// AdoptNetnsRuntime (persisted starttime != live starttime). If a future
// change made backfill (or AdoptNetnsRuntime) skip the starttime comparison
// for backfilled identities specifically, this test would start passing a
// bad adoption and must fail.
func TestBackfillNetnsIdentity_ThenAdopt_StarttimeMismatch_Refuses(t *testing.T) {
	const wantSocket = "/tmp/nx3-backfill-mismatch.sock"
	const wantTap = "nx3g-mismatch"
	spawnFakeNetnsChild(t, wantSocket, wantTap)
	time.Sleep(20 * time.Millisecond)

	id, err := BackfillNetnsIdentity(os.Getpid(), wantSocket)
	if err != nil {
		t.Fatalf("BackfillNetnsIdentity: %v", err)
	}

	// Corrupt the persisted starttime the way a genuinely stale record
	// would present it: some value that is not what /proc reports now.
	corrupted := id.ChildStartTime + 1

	perimFile := socketpairFile(t)

	_, err = cloudhypervisor.AdoptNetnsRuntime(context.Background(), id.ChildPID, id.ChildPGID, corrupted, id.GuestTapName, id.APISocket, perimFile)
	if err == nil {
		t.Fatal("expected AdoptNetnsRuntime to refuse a backfilled identity whose starttime does not match /proc, got nil")
	}
}

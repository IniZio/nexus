// ch_virtiofs_zombie_test.go — count-based zombie-reaping proof for AC-12c.
//
// Acceptance criteria exercised:
//   AC-12c #1 — a managedProcess child that exits mid-life is reaped promptly;
//               no zombie remains. Proven by polling /proc/<pid>/stat field 3
//               on the REAL spawnVirtiofsd call site (N children, external kill).
//               TestSpawnVirtiofsd_NChildExternalKillZombieCount
//   AC-12c #2 — kill() returns only after the child is fully reaped.
//               TestManagedProcess_KillReapsZombie
//   AC-12c #3 — kill() must not signal a PID whose slot may be recycled:
//               the deathCh guard returns early before sending any signal.
//               Proven by killFn injection asserting zero invocations.
//               TestKillGuard_NoSignalAfterReap
//
// Mutation proof annotations are in each test's doc comment.
package cloudhypervisor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// zombieState reads /proc/<pid>/stat field 3 and returns ("Z", true) if the
// process is a zombie. Returns ("", false) when the file is absent (already
// reaped). A missing /proc entry is NOT a zombie — the process was reaped.
func zombieState(pid int) (state string, isZombie bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false // ENOENT or unreadable: process already reaped
	}
	closeIdx := bytes.LastIndexByte(raw, ')')
	if closeIdx < 0 || closeIdx+2 >= len(raw) {
		return "", false
	}
	fields := bytes.Fields(raw[closeIdx+1:])
	if len(fields) == 0 {
		return "", false
	}
	s := string(fields[0])
	return s, s == "Z"
}

// countZombies returns the number of PIDs in the list that are currently in
// zombie state. This is the count-based assertion demanded by AC-12c — it
// reads actual /proc state, not a channel or a mock.
func countZombies(pids []int) int {
	n := 0
	for _, pid := range pids {
		if _, z := zombieState(pid); z {
			n++
		}
	}
	return n
}

// TestSpawnVirtiofsd_NChildExternalKillZombieCount is the primary AC-12c
// count-based proof. It spawns N virtiofsd children through the REAL
// spawnVirtiofsdForMounts call site, kills one externally (simulating
// mid-life unexpected death), then polls /proc counting zombies until they
// are gone or the deadline passes. The count assertion speaks directly —
// it does not depend on deathCh at all.
//
// POSITIVE mutation proof: removing `go p.reapWatcher()` from newManagedProcess
// makes the killed child persist as a zombie throughout polling. The deadline
// fires, finalCount = 1, and t.Errorf fires — RED on the count assertion
// specifically. Substitution count: 1.
func TestSpawnVirtiofsd_NChildExternalKillZombieCount(t *testing.T) {
	const N = 3
	bin := fakeVirtiofsd(t) // creates socket then sleeps 300s
	mounts := make([]domain.LiveMount, N)
	for i := range mounts {
		mounts[i] = domain.LiveMount{
			HostPath:  t.TempDir(),
			GuestPath: fmt.Sprintf("/mnt%d", i),
		}
	}
	d := testDriver(t, bin, mounts)

	var id domain.SandboxID
	if _, err := d.spawnVirtiofsdForMounts(t.Context(), id); err != nil {
		t.Fatalf("spawnVirtiofsdForMounts: %v", err)
	}

	d.mu.Lock()
	procs := append([]*managedProcess(nil), d.virtiofsdProcs[id]...)
	d.mu.Unlock()

	pids := make([]int, len(procs))
	for i, p := range procs {
		pids[i] = p.pid
	}

	// Cleanup: use direct SIGKILL rather than mp.kill() to avoid blocking on
	// deathCh in mutation scenarios where no reapWatcher runs. The test
	// process exits after all tests complete; any orphaned children are
	// reparented to init which reaps them — acceptable for test teardown.
	t.Cleanup(func() {
		for _, p := range procs {
			_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		}
	})

	// Kill ONE child externally — only a pid we spawned. This simulates the
	// mid-life unexpected death scenario without going through the driver.
	target := procs[0]
	_ = syscall.Kill(-target.pid, syscall.SIGKILL)

	// Sleep long enough for the process to transition R→Z (zombie). SIGKILL is
	// delivered immediately by the kernel; 200ms is ample time for the process
	// to exit and for reapWatcher's Wait4 to reap it. Without the reapWatcher
	// (mutation), the zombie persists past this window.
	const zombieWindow = 200 * time.Millisecond
	time.Sleep(zombieWindow)

	// Poll /proc counting zombies. Do NOT gate on deathCh — the count must
	// speak independently of channel state so the mutation proof is clean.
	// With the reapWatcher running, Wait4 fires the instant the process exits,
	// so the zombie is gone by the time we reach this loop. Without the
	// reapWatcher (mutation), the zombie accumulates throughout.
	const pollDeadline = 5 * time.Second
	deadline := time.Now().Add(pollDeadline)
	var finalCount int
	for {
		finalCount = countZombies(pids)
		if finalCount == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if finalCount != 0 {
		t.Errorf("zombie count = %d after external mid-life kill of 1/%d virtiofsd "+
			"children (pid %d), want 0 within %s; reapWatcher failed to reap on the "+
			"real spawnVirtiofsd call site",
			finalCount, N, target.pid, zombieWindow+pollDeadline)
	}
}

// TestManagedProcess_KillReapsZombie verifies that kill() returns only after
// the child is fully reaped — the zombie count immediately after kill() is zero.
// This also exercises the CH process path: managedProcess is shared by both
// virtiofsd and cloud-hypervisor.
func TestManagedProcess_KillReapsZombie(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid

	mp := newManagedProcess(cmd, pid, nil)
	mp.kill() // sends SIGKILL and drains deathCh — returns only after reap

	// Immediately after kill() the zombie count must be zero.
	if n := countZombies([]int{pid}); n != 0 {
		state, _ := zombieState(pid)
		t.Errorf("zombie count = %d, want 0; pid %d state=%q is still a zombie "+
			"after kill() returned", n, pid, state)
	}
}

// TestKillGuard_NoSignalAfterReap is the NEGATIVE mutation proof. It verifies
// the PID-recycling guard: once deathCh is closed (child reaped), kill() must
// return immediately without invoking killFn. A recycled PID could belong to
// an unrelated process; signalling it is the bug this guard exists to prevent.
//
// NEGATIVE mutation: remove the early `return` inside the `case <-p.deathCh:`
// branch of kill()'s guard select. killFn is then invoked after reap →
// invocations = 1 → t.Errorf fires → RED. Substitution count: 1.
func TestKillGuard_NoSignalAfterReap(t *testing.T) {
	// Count killFn invocations via injection.
	invocations := 0
	orig := killFn
	killFn = func(pid int, sig syscall.Signal) error {
		invocations++
		return orig(pid, sig)
	}
	t.Cleanup(func() { killFn = orig })

	// Spawn a child that exits immediately — deathCh will close quickly.
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	mp := newManagedProcess(cmd, pid, nil)

	// Wait for reapWatcher to close deathCh (process fully reaped).
	select {
	case <-mp.deathCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("deathCh did not close within 5 s for pid %d", pid)
	}

	// Now call kill() — the guard must see the closed deathCh and return
	// without invoking killFn.
	mp.kill()

	if invocations != 0 {
		t.Errorf("killFn invoked %d time(s) after deathCh was closed; "+
			"must return early to avoid signalling a recycled PID", invocations)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// reapSuspectFixture builds a fake procDir containing one synthetic netns-child
// entry (NEXUS3_NETNS_RUN=1 with an unparseable socket path) that the sweep
// will classify as Suspect: the process is owned by the test runner's uid
// (procDir is a t.TempDir()), but the socket path does not parse as a sandbox
// ID, so reap cannot rule out that it is an unrecorded orphan.
func reapSuspectFixture(t *testing.T) (store.Store, *service.ResourceIndex, service.ReapOptions) {
	t.Helper()
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	// Synthetic pid that will be seen as "ours" because t.TempDir() is
	// owned by the test-runner uid.
	const suspectPID = 424246
	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", suspectPID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Not a sandbox-id socket path → sandboxIDFromSocketPath will fail →
	// classified as Suspect (fail-closed rail: cannot rule out orphan).
	const notASocket = "/run/some-foreign-daemon.sock"
	environ := "NEXUS3_NETNS_RUN=1\x00NEXUS3_NETNS_API_SOCKET=" + notASocket + "\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o600); err != nil {
		t.Fatalf("write environ: %v", err)
	}
	pgidLine := fmt.Sprintf("%d (sleep) S 1 %d 0 0 -1\n", suspectPID, suspectPID)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(pgidLine), 0o600); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})
	opts := service.ReapOptions{ProcDir: procDir}
	return st, idx, opts
}

// TestRunReapFull_SuspectExitsNonZero verifies the CLI contract at
// cmd_reap.go: when suspects > 0, runReapFull must return a non-zero exit
// code so that shell callers can distinguish a clean report from a degraded one.
//
// Mutation proof: make the suspect path return nil instead of &ExitCodeError →
// this test goes RED. Restore → GREEN.
func TestRunReapFull_SuspectExitsNonZero(t *testing.T) {
	st, idx, opts := reapSuspectFixture(t)
	out, _, _ := newTestOutput(false)

	err := runReapFull(context.Background(), st, idx, false /*dry-run*/, out, opts)
	if err == nil {
		t.Fatal("runReapFull returned nil despite suspects > 0; shell callers cannot detect a degraded report")
	}
	var exitErr *ExitCodeError
	if !asExitCodeError(err, &exitErr) {
		t.Fatalf("error is %T (%v), want *ExitCodeError so the shell sees a failure", err, err)
	}
	if exitErr.Code == 0 {
		t.Error("exit code is 0; suspects must produce a non-zero exit")
	}
}

// TestRunReapFull_SuspectInHumanOutput verifies the human-readable surface:
// suspects must be printed explicitly (not swallowed into "No orphaned resources
// found") so an operator can investigate.
func TestRunReapFull_SuspectInHumanOutput(t *testing.T) {
	st, idx, opts := reapSuspectFixture(t)
	out, stdout, _ := newTestOutput(false)

	// We only check output format here; ignore the non-zero exit from above.
	_ = runReapFull(context.Background(), st, idx, false, out, opts)

	body := stdout.String()
	if strings.Contains(body, "No orphaned resources found") {
		t.Error("human output says 'No orphaned resources found' despite suspects > 0 — suspects must not be silently folded into a clean report")
	}
	if !strings.Contains(strings.ToUpper(body), "SUSPECT") {
		t.Errorf("human output does not mention SUSPECT:\n%s", body)
	}
}

// reapUninspectableFixture builds a fake procDir where a process owned by the
// test runner's uid has an unreadable environ, standing in for a real process
// whose /proc/<pid>/environ cannot be read (cleared dumpable flag, or the
// process vanished mid-sweep). It yields a non-zero UninspectableProcesses
// count and zero Suspect entries.
//
// The unreadability is produced structurally (environ is a directory → EISDIR),
// not with mode bits — see the comment at the mkdir below for why that matters.
func reapUninspectableFixture(t *testing.T) (store.Store, *service.ResourceIndex, service.ReapOptions) {
	t.Helper()
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	const uninspPID = 424250
	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", uninspPID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Make /proc/<pid>/environ STRUCTURALLY unreadable by making it a directory:
	// os.ReadFile opens it fine and then read(2) fails with EISDIR, for every uid.
	//
	// DO NOT "simplify" this back to os.WriteFile + os.Chmod(0o000). A mode-0000
	// file is only unreadable for a uid that is subject to the DAC check. Root
	// holds CAP_DAC_OVERRIDE and reads it successfully, so under uid 0 the sweep
	// parses the environ and reports the pid as an ORPHAN — the assertions in the
	// tests below then cannot fail, and the coverage is vacuous. Every nexus3
	// sandbox runs its tests as uid 0, so the chmod form was silently dead on the
	// runner that matters most.
	//
	// EISDIR is a non-ENOENT error, so it still lands on exactly the branch these
	// tests exercise: the `if os.IsNotExist(envErr)` gate at reap.go:1107 falls
	// through to the `inaccessible++` arm at reap.go:1124.
	environPath := filepath.Join(pidDir, "environ")
	if err := os.Mkdir(environPath, 0o700); err != nil {
		t.Fatalf("mkdir environ (as a directory, to force EISDIR): %v", err)
	}
	// The fixture must be self-checking, not comment-checked. A comment cannot
	// fail; this can. It fires as root, and it is agnostic to HOW the file was
	// made unreadable — it survives a revert to chmod, and an ACL, and anything
	// else someone reaches for. Without it, a vacuous fixture goes green.
	if _, err := os.ReadFile(environPath); err == nil {
		t.Fatalf("fixture is vacuous: environ is READABLE as uid %d, so the "+
			"uninspectable branch will never run and the assertions below cannot fail",
			os.Getuid())
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})
	return st, idx, service.ReapOptions{ProcDir: procDir}
}

// TestRunReapFull_UninspectableExitsZero verifies that when
// UninspectableProcesses > 0 but there are ZERO suspects and ZERO orphans,
// runReapFull must return nil (exit 0). The uninspectable count
// must NOT drive a non-zero exit.
//
// Mutation proof: add `if report.UninspectableProcesses > 0 { return &ExitCodeError{Code:1} }`
// to runReapFull → this test goes RED (nil becomes non-nil). Restore → GREEN.
func TestRunReapFull_UninspectableExitsZero(t *testing.T) {
	st, idx, opts := reapUninspectableFixture(t)
	out, stdout, _ := newTestOutput(false)

	err := runReapFull(context.Background(), st, idx, false /*dry-run*/, out, opts)
	if err != nil {
		t.Fatalf("runReapFull returned %v; uninspectable-only run must exit 0", err)
	}

	// The output must mention the actual classification reason.
	// "inaccessible" is the keyword emitted for UninspectableProcesses > 0.
	body := stdout.String()
	if !strings.Contains(body, "inaccessible") {
		t.Errorf("human output does not describe uninspectable processes; got:\n%s", body)
	}
}

// TestRunReapFull_JSONSuspectExitsNonZero verifies the JSON surface: in JSON
// mode, suspects > 0 must still produce a non-zero exit so machine callers
// reading only the exit code see the failure.
//
// Mutation proof: same as TestRunReapFull_SuspectExitsNonZero — make the
// JSON-mode suspect branch return nil → this test goes RED.
func TestRunReapFull_JSONSuspectExitsNonZero(t *testing.T) {
	st, idx, opts := reapSuspectFixture(t)
	out, stdout, _ := newTestOutput(true /*json*/)

	err := runReapFull(context.Background(), st, idx, false, out, opts)
	if err == nil {
		t.Fatal("JSON-mode runReapFull returned nil despite suspects > 0")
	}
	var exitErr *ExitCodeError
	if !asExitCodeError(err, &exitErr) {
		t.Fatalf("error is %T (%v), want *ExitCodeError", err, err)
	}
	if exitErr.Code == 0 {
		t.Error("exit code is 0; JSON-mode suspects must produce a non-zero exit")
	}

	// The JSON envelope must still be present and must contain the suspect entry.
	var env struct {
		Data struct {
			Entries []struct {
				Status string `json:"status"`
			} `json:"entries"`
		} `json:"data"`
	}
	if err2 := json.Unmarshal(stdout.Bytes(), &env); err2 != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err2, stdout.String())
	}
	var hasSuspect bool
	for _, e := range env.Data.Entries {
		if e.Status == "suspect" {
			hasSuspect = true
			break
		}
	}
	if !hasSuspect {
		t.Errorf("JSON entries do not contain a suspect entry:\n%s", stdout.String())
	}
}

// waitForProcGone polls syscall.Kill(pid, 0) until the process is gone (ESRCH)
// or the deadline elapses. Returns true when the process is gone, false when
// it is still alive at the deadline. Mirrors the same helper in
// internal/core/service/reap_netns_sweep_test.go (package service_test, not
// importable here).
func waitForProcGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return true // process is gone
		}
		if time.Now().After(deadline) {
			return false // still alive
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRunReapFull_ApplyLeavesSuspectAlive verifies that --apply does NOT kill a
// process classified as Suspect. A Suspect entry means reap cannot rule out
// that the process is legitimate; killing it would be data loss. This boundary
// has failed advisor gates twice — assert it, don't just read the code.
//
// Liveness check: cmd.Wait() is backgrounded immediately after Start() so a
// SIGKILL promptly causes the PID to disappear (no zombie). We then poll
// syscall.Kill(pid, 0) for ESRCH — a zombie returns nil, a truly-dead process
// returns ESRCH, so Signal(0) on a zombie would give a false "alive" verdict.
//
// Classification check: the JSON envelope must contain a "suspect" entry for
// the PID so a future change that stops emitting the entry at all would also
// go RED.
//
// Mutation proof: in service.Reap change `if entry.Status == ReapStatusOrphan && apply {`
// to `if apply {` (substitution count 1, compiles) → this test goes RED (the
// spawned sleep is gone). Restore → GREEN.
func TestRunReapFull_ApplyLeavesSuspectAlive(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	// Spawn a real process as its own process group so Kill(-pgid, SIGKILL)
	// would actually reach it if reap incorrectly tries to kill a Suspect.
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleeper: %v", err)
	}
	// Background Wait() so a SIGKILL promptly reaps the zombie and the PID
	// disappears. Without this, Signal(0) on a zombie returns nil — the test
	// would report "alive" whether or not reap killed it (the previous bug).
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	pid := cmd.Process.Pid

	// Write a synthetic /proc entry for the real PID: NEXUS3_NETNS_RUN=1 with
	// an unparseable socket path → classified Suspect (fail-closed: socket path
	// is not a sandbox ID, so reap cannot rule out orphan).
	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const notASocket = "/run/some-foreign-daemon.sock"
	environ := "NEXUS3_NETNS_RUN=1\x00NEXUS3_NETNS_API_SOCKET=" + notASocket + "\x00PATH=/usr/bin\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "environ"), []byte(environ), 0o600); err != nil {
		t.Fatalf("write environ: %v", err)
	}
	pgidLine := fmt.Sprintf("%d (sleep) S 1 %d 0 0 -1\n", pid, pid)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(pgidLine), 0o600); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})
	opts := service.ReapOptions{ProcDir: procDir}
	out, stdout, _ := newTestOutput(true /*json*/)

	// apply=true: reap should classify the entry as Suspect and leave it alive.
	_ = runReapFull(context.Background(), st, idx, true /*apply*/, out, opts)

	// Poll with ESRCH so we detect a truly-dead process, not a zombie.
	// 200 ms is enough for a SIGKILL to deliver and the process table to be
	// updated; a live Suspect has no reason to exit during this window.
	if waitForProcGone(pid, 200*time.Millisecond) {
		t.Errorf("Suspect process (pid %d) was killed by reap --apply; it must not be", pid)
	}

	// Also assert the entry was classified Suspect: a future change that stops
	// emitting the entry at all would make the liveness check vacuous again.
	var env struct {
		Data struct {
			Entries []struct {
				Status string `json:"status"`
			} `json:"entries"`
		} `json:"data"`
	}
	if err2 := json.Unmarshal(stdout.Bytes(), &env); err2 != nil {
		t.Fatalf("unmarshal reap report: %v\n%s", err2, stdout.String())
	}
	hasSuspect := false
	for _, e := range env.Data.Entries {
		if e.Status == "suspect" {
			hasSuspect = true
			break
		}
	}
	if !hasSuspect {
		t.Errorf("no entry classified suspect; full report:\n%s", stdout.String())
	}
}

// TestRunReapFull_JSONCarriesUninspectableCount verifies that the JSON envelope
// includes the uninspectable_processes field, so machine callers can distinguish
// a degraded sweep from a clean one without parsing human output.
//
// Mutation proof: delete `UninspectableProcesses: report.UninspectableProcesses`
// from the JSON struct population in runReapFull → JSON field is 0, test goes
// RED. Restore → GREEN.
func TestRunReapFull_JSONCarriesUninspectableCount(t *testing.T) {
	st, idx, opts := reapUninspectableFixture(t)
	out, stdout, _ := newTestOutput(true /*json*/)

	if err := runReapFull(context.Background(), st, idx, false /*dry-run*/, out, opts); err != nil {
		t.Fatalf("runReapFull returned %v; uninspectable-only run must exit 0", err)
	}

	var env struct {
		Data struct {
			UninspectableProcesses int `json:"uninspectable_processes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err, stdout.String())
	}
	if env.Data.UninspectableProcesses == 0 {
		t.Errorf("JSON envelope has uninspectable_processes=0; want >0 when sweep encountered uninspectable processes:\n%s", stdout.String())
	}
}

// TestReap_AllowRealProcKillGuard verifies that service.Reap with apply=true
// and a default ProcDir (real /proc) returns an error naming AllowRealProcKill.
// Without this guard a test that forgets to set ProcDir would silently send
// SIGKILL to live host processes.
//
// Mutation proof: delete the `if apply && opt.ProcDir == "/proc" &&
// !opt.AllowRealProcKill {` block in service.Reap → this test goes RED (nil
// error). Restore → GREEN.
func TestReap_AllowRealProcKillGuard(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: t.TempDir(),
		SocketDir: t.TempDir(),
	})
	// apply=true with no ProcDir set — must be rejected.
	_, err = service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{})
	if err == nil {
		t.Fatal("Reap(apply=true, ProcDir default) must return an error; got nil")
	}
	if !strings.Contains(err.Error(), "AllowRealProcKill") {
		t.Errorf("error %q does not name AllowRealProcKill", err.Error())
	}
}

// reapZombieFixture builds a synthetic procDir containing one zombie process
// (stat state == "Z") owned by the test runner. reap must count it as
// ZombieProcesses, not UninspectableProcesses, and emit zero ReapEntries.
func reapZombieFixture(t *testing.T) (store.Store, *service.ResourceIndex, service.ReapOptions) {
	t.Helper()
	stateRoot := t.TempDir()
	sockDir := t.TempDir()
	procDir := t.TempDir()

	const zombiePID = 424252
	pidDir := filepath.Join(procDir, fmt.Sprintf("%d", zombiePID))
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// State field (field 3) is "Z" → zombie.
	statContent := fmt.Sprintf("%d (virtiofsd) Z 1 %d 0 0 -1\n", zombiePID, zombiePID)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent), 0o600); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	// No environ file: zombies have no mm, so the file would be absent.

	st, err := store.NewFileStore(stateRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})
	return st, idx, service.ReapOptions{ProcDir: procDir}
}

// TestRunReapFull_JSONCarriesZombieCount verifies that the JSON envelope
// includes zombie_processes as a distinct field (not merged into
// uninspectable_processes), so machine callers can track the virtiofsd/nexus3
// Wait() leak independently from other uninspectable processes.
//
// Mutation proof: in sweepOrphanNetnsProcesses change the zombie branch from
// `zombies++` to `inaccessible++` → zombie_processes is 0 in JSON, test goes
// RED. Restore → GREEN.
func TestRunReapFull_JSONCarriesZombieCount(t *testing.T) {
	st, idx, opts := reapZombieFixture(t)
	out, stdout, _ := newTestOutput(true /*json*/)

	if err := runReapFull(context.Background(), st, idx, false /*dry-run*/, out, opts); err != nil {
		t.Fatalf("runReapFull returned %v; zombie-only run must exit 0", err)
	}

	var env struct {
		Data struct {
			ZombieProcesses        int `json:"zombie_processes"`
			UninspectableProcesses int `json:"uninspectable_processes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err, stdout.String())
	}
	if env.Data.ZombieProcesses == 0 {
		t.Errorf("JSON envelope has zombie_processes=0; want >0:\n%s", stdout.String())
	}
	if env.Data.UninspectableProcesses != 0 {
		t.Errorf("JSON envelope has uninspectable_processes=%d; zombie must not bleed into inaccessible count:\n%s",
			env.Data.UninspectableProcesses, stdout.String())
	}
}

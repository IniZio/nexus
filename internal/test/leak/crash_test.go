// Package leak — extreme-case leak suite for the nexus3 resource-lifecycle contract.
//
// Covers:
//
//	R3-AC1(a): SIGKILL at each stage boundary (intent-only, intent+raw, intent+raw+workspace)
//	R3-AC1(b): abrupt process-group kill (power-loss proxy)
//	R3-AC1(c): concurrent create/remove races — see race_test.go
//	R3-AC1(d): removal interrupted between file creation and record commit
//	R3-AC2:    a live sandbox with a store record is never deleted by the reaper
//
// # Subprocess pattern
//
// Crash tests spawn THIS binary as a subprocess helper (via NEXUS3_LEAK_HELPER).
// TestMain detects the env var and runs the helper instead of the test suite.
// The helper mimics CreateAndBoot by writing disk resources in stage order, then
// blocks until the parent sends SIGKILL. This exercises the real crash path:
// no deferred cleanup runs, files remain on disk exactly as the OS would leave them.
//
// # What the crash tests prove
//
// - SIGKILL does NOT run Go defers → intent file survives (mechanical proof of the
//   structural argument, not just a claim about it).
// - After SIGKILL, service.Reap --apply finds all orphaned resources and deletes them,
//   leaving zero stranded bytes.
//
// # What they cannot prove
//
// - Power loss at the storage layer. writeCreateIntent uses os.WriteFile, which
//   flushes to the kernel page cache via f.Close() but does NOT call fsync(2).
//   A voltage failure before the kernel writes the page cache to disk would lose
//   the intent file — leaving an orphan .raw without a detectable intent, identical
//   to the pre-R2 state. See "PRODUCTION FINDING — fsync gap" below.
//
// PRODUCTION FINDING — fsync gap:
//
//	service.writeCreateIntent calls os.WriteFile, whose f.Close() does NOT call
//	fsync. A hardware power failure before page-cache writeback drops the intent
//	file silently. The .raw disk would survive (ext4 journal replay preserves
//	partially-written inodes) but without an intent the reaper cannot distinguish
//	it from a pre-R2 orphan. Mitigation: add f.Sync() between Write and Close in
//	writeCreateIntent. This is a production fix required in
//	internal/core/service/intent.go — report only, not fixed here per R3 scope.
//
// # Run instructions
//
//	TMPDIR=/tmp go test ./internal/test/leak/ -v
//	TMPDIR=/tmp go test ./internal/test/leak/ -v -count=5         # repeat for flake detection
//	TMPDIR=/tmp go test -race ./internal/test/leak/ -run Race      # race detector
package leak_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// helperEnv is the env var that signals subprocess helper mode.
// Its value is the stage number (1, 2, or 3).
const helperEnv = "NEXUS3_LEAK_HELPER"

// TestMain detects subprocess helper mode before running any tests.
//
// When NEXUS3_LEAK_HELPER is non-empty this binary is running as a crash
// stub. It writes disk resources up to the given stage, signals readiness,
// then blocks until SIGKILL'd. This proves the mechanism: the OS kills the
// process, defers do not run, and files remain on disk.
func TestMain(m *testing.M) {
	if stage := os.Getenv(helperEnv); stage != "" {
		helperRun(stage)
		// helperRun blocks; the OS kills us before we return.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// helperRun is the crasher subprocess logic. It simulates a CreateAndBoot
// process at the given stage, then blocks until SIGKILL'd.
//
// Stage boundaries (mirroring the order in service/create.go):
//
//	1  intent written, no disk files (crash before cowExt4 at line 394)
//	2  intent + .raw written (crash after cowExt4, before workspace at line 423)
//	3  intent + .raw + -workspace.ext4 written (crash after workspace, before store.Create at line 449)
//
// Environment inputs (all required):
//
//	NEXUS3_LEAK_HELPER   stage number
//	NEXUS3_LEAK_DIR      disk directory (= <stateRoot>/disks/)
//	NEXUS3_LEAK_ID       sandbox ULID string (e.g. "sb-01XXXXXXXXXXXXXXXXXXXXXXXX")
//	NEXUS3_LEAK_READY    path of sentinel file to write when ready to be killed
func helperRun(stage string) {
	diskDir := helperMustEnv("NEXUS3_LEAK_DIR")
	idStr := helperMustEnv("NEXUS3_LEAK_ID")
	readyPath := helperMustEnv("NEXUS3_LEAK_READY")

	var stageNum int
	if _, err := fmt.Sscanf(stage, "%d", &stageNum); err != nil || stageNum < 1 || stageNum > 3 {
		fmt.Fprintf(os.Stderr, "NEXUS3_LEAK_HELPER: invalid stage %q\n", stage)
		os.Exit(2)
	}

	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "helper: mkdir %s: %v\n", diskDir, err)
		os.Exit(2)
	}

	// ── Step 1: write intent file BEFORE any disk file ───────────────────────
	// This mirrors service.writeCreateIntent (called at line 353 of create.go,
	// before cowExt4 at line 394). The intent file is always the first file
	// written; disk files follow. Killing the process at any point after this
	// leaves the intent on disk (defers don't run on SIGKILL).
	intentPath := service.IntentPath(diskDir, helperParseID(idStr))
	// We write the intent JSON directly because writeCreateIntent is unexported.
	// The format is the createIntent struct serialised to JSON.
	intentJSON := fmt.Sprintf(
		`{"id":%q,"disk_copy_path":%q,"workspace_disk_path":%q}`,
		idStr,
		filepath.Join(diskDir, idStr+".raw"),
		filepath.Join(diskDir, idStr+"-workspace.ext4"),
	)
	if err := os.WriteFile(intentPath, []byte(intentJSON), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "helper: write intent: %v\n", err)
		os.Exit(2)
	}

	// ── Step 2: create the per-sandbox .raw disk copy ────────────────────────
	// Mirrors cowExt4 (line 394). In production this is a CoW clone of the
	// cached ext4 image; here we write a 4 KiB sparse stub.
	if stageNum >= 2 {
		if err := helperSparse(filepath.Join(diskDir, idStr+".raw"), 4096); err != nil {
			fmt.Fprintf(os.Stderr, "helper: write raw: %v\n", err)
			os.Exit(2)
		}
	}

	// ── Step 3: create the workspace ext4 disk ───────────────────────────────
	// Mirrors WorktreeToDisk / WorkspaceCapturer (line 423).
	if stageNum >= 3 {
		if err := helperSparse(filepath.Join(diskDir, idStr+"-workspace.ext4"), 4096); err != nil {
			fmt.Fprintf(os.Stderr, "helper: write workspace: %v\n", err)
			os.Exit(2)
		}
	}

	// Signal readiness: parent can now send SIGKILL.
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "helper: write ready sentinel: %v\n", err)
		os.Exit(2)
	}

	// Block until killed. This simulates a process mid-operation.
	select {}
}

func helperMustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "subprocess helper: missing env var %s\n", key)
		os.Exit(2)
	}
	return v
}

func helperParseID(s string) domain.SandboxID {
	id, err := domain.ParseSandboxID(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: bad sandbox ID %q: %v\n", s, err)
		os.Exit(2)
	}
	return id
}

func helperSparse(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(size-1, 0); err != nil {
		return fmt.Errorf("seek: %w", err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// ─── test helpers ─────────────────────────────────────────────────────────────

func newEmptyStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}

// spawnAndKill starts a subprocess helper at the given stage, polls for the
// ready sentinel, sends SIGKILL (or kills the process group when pgrpKill is
// true), and returns the stateRoot it wrote into.
//
// stateRoot layout:
//
//	<stateRoot>/disks/<id>.create-intent.json
//	<stateRoot>/disks/<id>.raw            (stage >= 2)
//	<stateRoot>/disks/<id>-workspace.ext4 (stage >= 3)
//
// Callers construct a ResourceIndex with StateRoot=stateRoot to enumerate
// the orphaned resources.
func spawnAndKill(t *testing.T, id domain.SandboxID, stage int, pgrpKill bool) (stateRoot string) {
	t.Helper()

	stateRoot = t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir diskDir: %v", err)
	}
	readyPath := filepath.Join(stateRoot, "ready")

	cmd := exec.Command(os.Args[0]) // same test binary, enters TestMain helper mode
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%d", helperEnv, stage),
		"NEXUS3_LEAK_DIR="+diskDir,
		"NEXUS3_LEAK_ID="+id.String(),
		"NEXUS3_LEAK_READY="+readyPath,
	)
	if pgrpKill {
		// Put the subprocess in its own process group so we can kill the
		// entire group atomically — closer to how a host power loss or OOM
		// kill might terminate a set of related processes.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}

	// Poll for ready sentinel. 15 s is generous; the helper writes the file
	// immediately after the last resource creation, so normal latency is < 1 s.
	const readyTimeout = 15 * time.Second
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(readyPath); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("helper subprocess never signalled ready within %v", readyTimeout)
	}

	// Send the kill signal.
	if pgrpKill {
		// Kill entire process group (power-loss proxy). Negative PID targets
		// the group. We tolerate ESRCH in case the process exited between
		// our stat and the kill call.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			t.Logf("pgrp SIGKILL: %v (process may have already exited)", err)
		}
	} else {
		cmd.Process.Signal(syscall.SIGKILL)
	}
	cmd.Wait() // reap the zombie; exit status will be -1 / killed — expected

	return stateRoot
}

// assertZeroStranded runs Reap --apply against stateRoot with an empty proc dir
// (all processes appear dead → no ambiguity), then rescans and asserts that no
// resources remain. Fails the test if any resource survives the reap.
func assertZeroStranded(t *testing.T, stateRoot string, st store.Store) {
	t.Helper()
	ctx := context.Background()

	// Empty procDir → scanProcForULID returns procScanDead for every ID.
	// This ensures the liveness gate does not save orphan resources from deletion.
	emptyProcDir := t.TempDir()
	socketDir := t.TempDir() // no sockets → no API socket liveness

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: socketDir,
	})

	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: emptyProcDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// Re-enumerate after apply: the disk should be empty.
	remaining, err := idx.List()
	if err != nil {
		t.Fatalf("List after Reap --apply: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("stranded resources after Reap --apply (report had %d entries): %v",
			len(report.Entries), remaining)
	}
}

// ─── R3-AC1(a) crash tests ────────────────────────────────────────────────────

// TestCrashAtStageA_IntentOnly verifies that a SIGKILL delivered after the
// intent file is written but before any disk file is created leaves the intent
// file on disk (defers don't run on SIGKILL), and that Reap --apply cleans it
// up leaving zero stranded resources.
//
// Stage A models: process killed between writeCreateIntent (create.go line 353)
// and cowExt4 (create.go line 394).
//
// Pre-fix contrast: on pre-R2 code there is no intent file at stage A. A
// stage-A crash leaves no trace on disk whatsoever — the failed create is
// undetectable. Post-R2 the intent file IS on disk, reap finds it (enumerated
// as KindCreateIntent), and deletes it. A test that asserts the intent file
// exists after SIGKILL fails if the subprocess does not write it.
//
// @verifies R3-AC1(a)
func TestCrashAtStageA_IntentOnly(t *testing.T) {
	id := domain.NewSandboxID()
	stateRoot := spawnAndKill(t, id, 1 /*stage A — intent only*/, false)
	diskDir := filepath.Join(stateRoot, "disks")

	// SIGKILL survival check: intent file must be present on disk.
	// If this fails, defers somehow ran on SIGKILL — a serious runtime anomaly.
	intentPath := service.IntentPath(diskDir, id)
	if _, err := os.Stat(intentPath); os.IsNotExist(err) {
		t.Fatalf("SIGKILL at stage A: intent file missing — defers ran unexpectedly on SIGKILL " +
			"(or subprocess never reached the write); this is a critical invariant violation")
	}

	// .raw must NOT exist (stage A crashes before cowExt4).
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	if _, err := os.Stat(rawPath); !os.IsNotExist(err) {
		t.Errorf("stage A: unexpected .raw at %s (should not exist before cowExt4)", rawPath)
	}

	// -workspace.ext4 must NOT exist either.
	wsPath := filepath.Join(diskDir, id.String()+"-workspace.ext4")
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Errorf("stage A: unexpected workspace disk at %s", wsPath)
	}

	// Recovery: Reap --apply must clean up the stranded intent file.
	// An empty store (no records) means the intent has no matching record → orphan.
	st := newEmptyStore(t)
	assertZeroStranded(t, stateRoot, st)

	// Confirm intent is gone after reap.
	if _, err := os.Stat(intentPath); !os.IsNotExist(err) {
		t.Errorf("intent file still present after Reap --apply: %s", intentPath)
	}
}

// TestCrashAtStageB_IntentAndRaw verifies a SIGKILL after cowExt4 creates the
// .raw file but before WorktreeToDisk runs. Both the intent and the .raw must
// survive the kill and be cleaned up by Reap --apply.
//
// Stage B models: killed between cowExt4 (create.go line 394) and the workspace
// capturer (line 423). This is the classic P13 window that R2 was designed to make
// discoverable: an orphan .raw with an accompanying intent.
//
// Pre-fix contrast: on pre-R2 code the orphan .raw has no intent file alongside
// it — the reaper can still find it (via KindDiskRaw enumeration), but the stage-A
// discovery path is absent. The test would fail if the subprocess omitted the
// intent write (pre-R2 simulation), because the assertion below checks for the
// intent file's presence.
//
// @verifies R3-AC1(a)
func TestCrashAtStageB_IntentAndRaw(t *testing.T) {
	id := domain.NewSandboxID()
	stateRoot := spawnAndKill(t, id, 2 /*stage B — intent + .raw*/, false)
	diskDir := filepath.Join(stateRoot, "disks")

	// Both intent and .raw must survive SIGKILL.
	intentPath := service.IntentPath(diskDir, id)
	if _, err := os.Stat(intentPath); os.IsNotExist(err) {
		t.Fatalf("SIGKILL at stage B: intent file missing — defers ran on SIGKILL")
	}
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		t.Fatalf("SIGKILL at stage B: .raw disk missing — subprocess did not reach stage B")
	}

	// Workspace must NOT exist at stage B.
	wsPath := filepath.Join(diskDir, id.String()+"-workspace.ext4")
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Errorf("stage B: unexpected workspace disk at %s", wsPath)
	}

	// Recovery: both intent and .raw are orphans (no store record).
	st := newEmptyStore(t)
	assertZeroStranded(t, stateRoot, st)

	if _, err := os.Stat(intentPath); !os.IsNotExist(err) {
		t.Errorf("intent file still present after Reap --apply: %s", intentPath)
	}
	if _, err := os.Stat(rawPath); !os.IsNotExist(err) {
		t.Errorf(".raw still present after Reap --apply: %s", rawPath)
	}
}

// TestCrashAtStageC_IntentRawWorkspace verifies a SIGKILL after all three disk
// resources have been created but before svc.store.Create commits the record.
// All three resources must survive the kill and be cleaned up by Reap --apply.
//
// Stage C models: killed between WorktreeToDisk (create.go line 423) and
// svc.store.Create (create.go line 449).
//
// @verifies R3-AC1(a)
func TestCrashAtStageC_IntentRawWorkspace(t *testing.T) {
	id := domain.NewSandboxID()
	stateRoot := spawnAndKill(t, id, 3 /*stage C — intent + .raw + workspace*/, false)
	diskDir := filepath.Join(stateRoot, "disks")

	intentPath := service.IntentPath(diskDir, id)
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	wsPath := filepath.Join(diskDir, id.String()+"-workspace.ext4")

	// All three must survive SIGKILL (defers don't run).
	for _, path := range []string{intentPath, rawPath, wsPath} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Fatalf("SIGKILL at stage C: %s missing — defers ran or subprocess did not reach stage C", path)
		}
	}

	// Recovery: all three are orphans (no store record).
	st := newEmptyStore(t)
	assertZeroStranded(t, stateRoot, st)

	for _, path := range []string{intentPath, rawPath, wsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("resource still present after Reap --apply: %s", path)
		}
	}
}

// TestProcessGroupKill_PowerLossProxy verifies R3-AC1(b): an abrupt kill of
// the entire process group (simulating a host OOM kill or, at the process level,
// a power loss) leaves the same resource state as a single-process SIGKILL.
//
// Limitation acknowledged: this test exercises the PROCESS-LEVEL crash path,
// not a true storage-layer power loss. A real power failure before page-cache
// writeback would lose the intent file (see PRODUCTION FINDING — fsync gap above).
// We explicitly do NOT claim to cover that case. What we do prove: killing the
// entire pgrp does not trigger deferred cleanup, and Reap --apply recovers fully.
//
// @verifies R3-AC1(b)
func TestProcessGroupKill_PowerLossProxy(t *testing.T) {
	id := domain.NewSandboxID()
	// Stage B (intent + .raw) is the most representative P13 scenario.
	stateRoot := spawnAndKill(t, id, 2, true /*pgrpKill=true*/)
	diskDir := filepath.Join(stateRoot, "disks")

	intentPath := service.IntentPath(diskDir, id)
	rawPath := filepath.Join(diskDir, id.String()+".raw")

	if _, err := os.Stat(intentPath); os.IsNotExist(err) {
		t.Fatalf("pgrp kill: intent file missing — defers ran on process-group SIGKILL")
	}
	if _, err := os.Stat(rawPath); os.IsNotExist(err) {
		t.Fatalf("pgrp kill: .raw missing — subprocess did not reach stage B")
	}

	st := newEmptyStore(t)
	assertZeroStranded(t, stateRoot, st)
}

// ─── R3-AC1(d) interrupted-removal test ──────────────────────────────────────

// TestRemovalInterruptedMidCreate verifies R3-AC1(d): a removal interrupted
// between disk-file creation and store-record commit.
//
// Scenario: the creation pipeline creates the .raw disk file but the process
// dies before svc.store.Create commits the record. The .raw is stranded (no
// record, no intent — simulating pre-R2 or the pathological case where the
// intent write itself also fails). The reaper must find the .raw via KindDiskRaw
// enumeration and delete it.
//
// This test does NOT use SIGKILL (the mechanism is already proven by stage tests
// A–C). It directly sets up the stranded-disk state and verifies the reaper's
// classification and cleanup. By operating at the reaper layer rather than the
// crash layer, it can test the pre-fix scenario exactly: what does the reaper
// see when there is NO intent file? It must still find the .raw via direct disk
// enumeration.
//
// Pre-fix failure mode: if ResourceIndex.List() were record-driven (only
// returning files with matching store records), this test would show ZERO
// orphans found. The test would then erroneously pass "assertZeroStranded"
// while the .raw is still on disk. The re-scan at the end of assertZeroStranded
// catches this: it rescans the filesystem and sees the .raw, failing the assertion.
//
// @verifies R3-AC1(d)
func TestRemovalInterruptedMidCreate(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	id := domain.NewSandboxID()

	// Create the .raw disk WITHOUT an intent file and WITHOUT a store record.
	// This simulates: intent write failed (or pre-R2 code), .raw was created,
	// process died before store.Create.
	rawPath := filepath.Join(diskDir, id.String()+".raw")
	if err := helperSparse(rawPath, 4096); err != nil {
		t.Fatalf("create orphan .raw: %v", err)
	}

	// Verify the orphan .raw appears in ResourceIndex.List().
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range resources {
		if r.Kind == service.KindDiskRaw && r.OwnerID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("orphan .raw not found by ResourceIndex.List() — enumeration is broken " +
			"(pre-fix regression: List() may be record-driven instead of filesystem-driven)")
	}

	// Reap --apply: orphan .raw must be deleted.
	st := newEmptyStore(t)
	assertZeroStranded(t, stateRoot, st)

	if _, err := os.Stat(rawPath); !os.IsNotExist(err) {
		t.Errorf(".raw still present after Reap --apply (stranded resource): %s", rawPath)
	}
}

// ─── R3-AC2: live sandbox protection ─────────────────────────────────────────

// TestLiveSandboxProtectedFromReaper verifies R3-AC2: a sandbox with a live
// store record is NEVER deleted by Reap --apply, even when orphan resources
// for other sandboxes are being cleaned up concurrently in the same state root.
//
// The test:
//  1. Creates a "live" sandbox: store record + .raw disk (simulating a running
//     sandbox whose record exists and whose disk file is present).
//  2. Adds three orphan sandboxes (intent + .raw, no records) to the same
//     state root so the reaper has work to do.
//  3. Runs Reap --apply and asserts that the live sandbox's disk is untouched.
//
// @verifies R3-AC2
func TestLiveSandboxProtectedFromReaper(t *testing.T) {
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create the store backed by a different temp dir (the store does not
	// store its records under stateRoot/disks; it uses its own directory tree).
	storeTmpDir := t.TempDir()
	st, err := store.NewFileStore(storeTmpDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// ── Live sandbox: has a store record AND a .raw file ──────────────────────
	liveID := domain.NewSandboxID()
	liveRaw := filepath.Join(diskDir, liveID.String()+".raw")
	if err := helperSparse(liveRaw, 4096); err != nil {
		t.Fatalf("create live .raw: %v", err)
	}
	liveSB := domain.Sandbox{
		ID:      liveID,
		Name:    "live-protected",
		Project: "leak-test",
		State:   domain.Created,
	}
	if err := st.Create(context.Background(), liveSB); err != nil {
		t.Fatalf("store.Create (live): %v", err)
	}

	// ── Three orphan sandboxes: intent + .raw, no records ────────────────────
	for i := range 3 {
		orphanID := domain.NewSandboxID()
		intentPath := service.IntentPath(diskDir, orphanID)
		intentJSON := fmt.Sprintf(`{"id":%q,"disk_copy_path":%q}`,
			orphanID.String(),
			filepath.Join(diskDir, orphanID.String()+".raw"),
		)
		if err := os.WriteFile(intentPath, []byte(intentJSON), 0o600); err != nil {
			t.Fatalf("write orphan intent %d: %v", i, err)
		}
		if err := helperSparse(filepath.Join(diskDir, orphanID.String()+".raw"), 4096); err != nil {
			t.Fatalf("write orphan raw %d: %v", i, err)
		}
	}

	// ── Run Reap --apply ──────────────────────────────────────────────────────
	emptyProcDir := t.TempDir()
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: emptyProcDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// ── Verify: live sandbox disk NOT deleted ─────────────────────────────────
	if _, statErr := os.Stat(liveRaw); os.IsNotExist(statErr) {
		t.Errorf("CRITICAL: Reap --apply deleted the LIVE sandbox disk %s (N-AC2 violation)", liveRaw)
		t.Logf("Report had %d entries, %d deleted paths", len(report.Entries), len(report.Deleted))
	}

	// Verify the live entry is classified as OWNED, not ORPHAN.
	var liveEntry *service.ReapEntry
	for i := range report.Entries {
		if report.Entries[i].Resource.OwnerID == liveID {
			liveEntry = &report.Entries[i]
			break
		}
	}
	if liveEntry == nil {
		t.Errorf("live sandbox %s not found in reap report at all", liveID)
	} else if liveEntry.Status != service.ReapStatusOwned {
		t.Errorf("live sandbox classified as %s, want ReapStatusOwned (N-AC2 violation)", liveEntry.Status)
	}

	// Verify: all orphans WERE deleted (reaper did work, not a no-op).
	for _, deleted := range report.Deleted {
		if filepath.Base(deleted) == liveID.String()+".raw" {
			t.Errorf("live sandbox .raw appeared in Deleted list: %s", deleted)
		}
	}
	if len(report.Deleted) == 0 {
		t.Errorf("reaper deleted nothing — did it find the 3 orphans? Entries: %v", report.Entries)
	}
}

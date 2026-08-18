//go:build integration

package cloudhypervisor

// fork_isolation_integration_test.go: live proof that sibling forks from the
// same snapshot have independent root disks.
//
// # What this tests
//
//   - TestForkDiskIsolation: boots a microVM from a raw ext4 disk image,
//     takes a snapshot, forks TWO children (childA, childB) from that snapshot,
//     and proves four isolation properties:
//
//  1. Both sibling forks boot healthy and the in-guest agent is reachable.
//  2. A write to /tmp/marker on childA is absent on childB (isolation A→B).
//  3. A write to /tmp/marker on childB is absent on childA (isolation B→A).
//  4. The parent's original disk file (ext4Path) is unmodified after ForkFrom
//     and after the children have written to their own disks.
//
//  Extra:
//  5. Distinct per-child .raw disk files exist in diskDir (one per child,
//     both different from the parent's disk path).
//
// The test adds two tiny binaries to the disk rootfs:
//   - /bin/write-marker: reads stdin → writes to /tmp/marker
//   - /bin/read-marker:  reads /tmp/marker → prints to stdout; exits 1 if absent
//
// # Guard conditions (same as TestDiskBoot)
//
//   - /dev/kvm
//   - cloud-hypervisor binary
//   - mke2fs
//   - testdata/vmlinux-x86_64
//
// # Running
//
//	bash scripts/fetch-boot-artifacts.sh
//	TMPDIR=/tmp go test -tags integration -run TestForkDiskIsolation \
//	    ./internal/core/driver/cloudhypervisor/ -v -timeout 360s

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// ── marker binary helpers ──────────────────────────────────────────────────

// writeMarkerSrc is a static Linux/amd64 binary that reads all stdin and
// writes it to /tmp/marker in the guest.
const writeMarkerSrc = `package main

import (
	"io"
	"os"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile("/tmp/marker", data, 0o644); err != nil {
		os.Exit(2)
	}
}
`

// readMarkerSrc is a static Linux/amd64 binary that reads /tmp/marker and
// prints it to stdout. Exits 1 if the file is absent.
const readMarkerSrc = `package main

import (
	"fmt"
	"os"
)

func main() {
	data, err := os.ReadFile("/tmp/marker")
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("ABSENT")
			os.Exit(1)
		}
		os.Exit(2)
	}
	fmt.Print(string(data))
}
`

// buildMarkerBin compiles src as a static Linux/amd64 binary and returns its
// path. The binary is cleaned up when t completes.
func buildMarkerBin(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	srcFile := filepath.Join(dir, name+".go")
	binFile := filepath.Join(dir, name)
	if err := os.WriteFile(srcFile, []byte(src), 0o600); err != nil {
		t.Fatalf("write %s.go: %v", name, err)
	}
	cmd := exec.Command("go", "build", "-o", binFile, srcFile)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", name, err, out)
	}
	return binFile
}

// copyExecutable copies src to dst with mode 0o755.
func copyExecutable(dst, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// ── Exec helpers ───────────────────────────────────────────────────────────

// forkExec runs a command in the given child VM and returns stdout. It marks
// the test as failed if the Exec call itself errors out (not the guest exit
// code — use forkExecCode for that).
func forkExec(t *testing.T, drv *CHDriver, id domain.SandboxID, argv []string, stdin io.Reader) (string, int32) {
	t.Helper()
	c := agent.NewClient(drv, id)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var stdout strings.Builder
	exitCode, err := c.Exec(ctx, agent.ExecOptions{
		Argv:   argv,
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Errorf("Exec(%v) on %s: %v", argv, id, err)
		return "", -1
	}
	return stdout.String(), exitCode
}

// ── test ───────────────────────────────────────────────────────────────────

func TestForkDiskIsolation(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	skipUnlessMke2fs(t)

	// build guest binaries
	t.Log("building guest binaries...")
	agentBin := buildNexus3Agent(t)
	helloBin := buildHelloBinForDisk(t)
	writeMarkerBin := buildMarkerBin(t, "write-marker", writeMarkerSrc)
	readMarkerBin := buildMarkerBin(t, "read-marker", readMarkerSrc)
	t.Log("guest binaries ready")

	// assemble rootfs
	rootfsDir := buildRootfsForDisk(t, agentBin, helloBin)
	// Inject the marker binaries into /bin so the guest agent can exec them.
	if err := copyExecutable(filepath.Join(rootfsDir, "bin", "write-marker"), writeMarkerBin); err != nil {
		t.Fatalf("inject write-marker: %v", err)
	}
	if err := copyExecutable(filepath.Join(rootfsDir, "bin", "read-marker"), readMarkerBin); err != nil {
		t.Fatalf("inject read-marker: %v", err)
	}

	// build ext4
	ext4Path := buildExt4Image(t, rootfsDir)
	t.Logf("ext4 image: %s", ext4Path)

	// socket dir (must be in /tmp for sun_path limit)
	socketDir, err := os.MkdirTemp("/tmp", "ch-fiso-")
	if err != nil {
		t.Fatalf("MkdirTemp (socketDir): %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	if len(socketDir)+35 > 107 {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socketDir too long for unix socket: %s", socketDir)
	}

	snapDir, err := os.MkdirTemp("/tmp", "ch-fiso-snap-")
	if err != nil {
		t.Fatalf("MkdirTemp (snapDir): %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(snapDir) })

	serialPath := filepath.Join(socketDir, "serial.log")

	// driver
	drv, err := New(Config{
		BinaryPath:       chBin,
		SocketDir:        socketDir,
		SnapshotDir:      snapDir,
		KernelPath:       kernelPath,
		DiskImagePath:    ext4Path,
		SerialOutputPath: serialPath,
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New CHDriver: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	defer cancel()

	// start parent VM
	parentID := domain.NewSandboxID()
	var parentVMMPID int

	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && t.Failed() {
			t.Logf("serial log:\n%s", string(content))
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = drv.Stop(stopCtx, parentID)
		if parentVMMPID != 0 {
			_ = syscall.Kill(-parentVMMPID, syscall.SIGKILL)
		}
		drv.clearState(parentID)
	})

	iid, err := drv.Start(ctx, driver.StartRequest{SandboxID: parentID})
	if err != nil {
		t.Fatalf("Start (parent): %v", err)
	}
	t.Logf("parent started: instanceID=%s", iid)

	drv.mu.Lock()
	if proc := drv.procs[parentID]; proc != nil {
		parentVMMPID = proc.pid
	}
	drv.mu.Unlock()

	obs := pollForState(t, drv, parentID, driver.Running, 30*time.Second)
	if obs.State != driver.Running {
		t.Fatalf("parent VM did not reach Running: %v (%s)", obs.State, obs.Detail)
	}
	t.Log("waiting for parent agent on vsock...")
	waitForAgentReady(t, drv, parentID, 30*time.Second)
	t.Log("parent agent ready")

	// snapshot
	snap, err := drv.TakeSnapshot(ctx, parentID, artifact.KindTransient)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}
	t.Logf("snapshot: ID=%s size=%d", snap.ID, snap.Size)

	obs = pollForState(t, drv, parentID, driver.Running, 15*time.Second)
	if obs.State != driver.Running {
		t.Errorf("parent not Running after snapshot: %v (%s)", obs.State, obs.Detail)
	}

	// stop parent VM
	// Stop the parent so its disk writes quiesce before we record the baseline
	// stat. The children fork from the snapshot, independent of the parent.
	{
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = drv.Stop(stopCtx, parentID)
		stopCancel()
		drv.clearState(parentID)
		parentVMMPID = 0 // clearState already removed it; avoid double-kill
	}

	// parent disk baseline stat
	parentStatBefore, err := os.Stat(ext4Path)
	if err != nil {
		t.Fatalf("stat parent disk before fork: %v", err)
	}
	parentMtimeBefore := parentStatBefore.ModTime()
	parentSizeBefore := parentStatBefore.Size()
	t.Logf("parent disk (baseline): size=%d mtime=%v", parentSizeBefore, parentMtimeBefore)

	// fork two children
	childAID := domain.NewSandboxID()
	childBID := domain.NewSandboxID()

	diskDir := filepath.Dir(ext4Path)
	childADisk := filepath.Join(diskDir, childAID.String()+".raw")
	childBDisk := filepath.Join(diskDir, childBID.String()+".raw")

	var childAVMMPID, childBVMMPID int
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = drv.Stop(stopCtx, childAID)
		_ = drv.Stop(stopCtx, childBID)
		if childAVMMPID != 0 {
			_ = syscall.Kill(-childAVMMPID, syscall.SIGKILL)
		}
		if childBVMMPID != 0 {
			_ = syscall.Kill(-childBVMMPID, syscall.SIGKILL)
		}
		drv.clearState(childAID)
		drv.clearState(childBID)
		_ = os.Remove(childADisk)
		_ = os.Remove(childBDisk)
	})

	t.Log("forking two children from snapshot...")
	instanceIDs, err := drv.ForkFrom(ctx, snap, []domain.SandboxID{childAID, childBID})
	if err != nil {
		t.Fatalf("ForkFrom: %v", err)
	}
	if len(instanceIDs) != 2 {
		t.Fatalf("ForkFrom returned %d instanceIDs, want 2", len(instanceIDs))
	}
	if instanceIDs[0] == "" || instanceIDs[1] == "" {
		t.Fatalf("ForkFrom returned empty instanceID: %v", instanceIDs)
	}
	t.Logf("childA instanceID=%s", instanceIDs[0])
	t.Logf("childB instanceID=%s", instanceIDs[1])

	drv.mu.Lock()
	if proc := drv.procs[childAID]; proc != nil {
		childAVMMPID = proc.pid
	}
	if proc := drv.procs[childBID]; proc != nil {
		childBVMMPID = proc.pid
	}
	drv.mu.Unlock()

	// proof 5: per-child disk files exist
	childAInfo, err := os.Stat(childADisk)
	if err != nil {
		t.Errorf("FAIL: childA disk %q absent: %v", childADisk, err)
	} else {
		t.Logf("PASS (proof 5): childA disk exists: %s (size=%d)", childADisk, childAInfo.Size())
	}
	childBInfo, err := os.Stat(childBDisk)
	if err != nil {
		t.Errorf("FAIL: childB disk %q absent: %v", childBDisk, err)
	} else {
		t.Logf("PASS (proof 5): childB disk exists: %s (size=%d)", childBDisk, childBInfo.Size())
	}

	// proof 1: both children boot healthy
	t.Log("waiting for childA to reach Running...")
	obs = pollForState(t, drv, childAID, driver.Running, 30*time.Second)
	if obs.State != driver.Running {
		t.Fatalf("childA did not reach Running: %v (%s)", obs.State, obs.Detail)
	}
	t.Log("waiting for childA agent on vsock...")
	waitForAgentReady(t, drv, childAID, 30*time.Second)
	t.Log("PASS (proof 1): childA boot healthy, agent reachable")

	t.Log("waiting for childB to reach Running...")
	obs = pollForState(t, drv, childBID, driver.Running, 30*time.Second)
	if obs.State != driver.Running {
		t.Fatalf("childB did not reach Running: %v (%s)", obs.State, obs.Detail)
	}
	t.Log("waiting for childB agent on vsock...")
	waitForAgentReady(t, drv, childBID, 30*time.Second)
	t.Log("PASS (proof 1): childB boot healthy, agent reachable")

	// proof 2: isolation A→B
	// Write a unique marker to childA's disk, then read it on childB.
	markerA := fmt.Sprintf("A-marker-%s\n", childAID.String()[:12])

	out, code := forkExec(t, drv, childAID, []string{"/bin/write-marker"}, strings.NewReader(markerA))
	if code != 0 {
		t.Errorf("write-marker on childA: exit %d, stdout=%q", code, out)
	}
	t.Logf("childA: wrote marker (exit %d)", code)

	// Read the marker back from childA to confirm the write worked.
	outA, _ := forkExec(t, drv, childAID, []string{"/bin/read-marker"}, nil)
	if !strings.Contains(outA, strings.TrimRight(markerA, "\n")) {
		t.Errorf("childA self-read: expected %q, got %q", strings.TrimRight(markerA, "\n"), outA)
	} else {
		t.Logf("childA self-read OK: %q", strings.TrimSpace(outA))
	}

	// Now read the same path on childB — it must NOT contain childA's marker.
	outB, codeB := forkExec(t, drv, childBID, []string{"/bin/read-marker"}, nil)
	if strings.Contains(outB, strings.TrimRight(markerA, "\n")) {
		t.Errorf("FAIL (proof 2): childB sees childA's marker — disk NOT isolated.\n  childA wrote: %q\n  childB read:  %q", markerA, outB)
	} else {
		t.Logf("PASS (proof 2): childB does not see childA's marker (exit=%d, out=%q)", codeB, strings.TrimSpace(outB))
	}

	// proof 3: isolation B→A
	// Write a different marker to childB's disk, then confirm childA doesn't see it.
	markerB := fmt.Sprintf("B-marker-%s\n", childBID.String()[:12])

	out, code = forkExec(t, drv, childBID, []string{"/bin/write-marker"}, strings.NewReader(markerB))
	if code != 0 {
		t.Errorf("write-marker on childB: exit %d, stdout=%q", code, out)
	}
	t.Logf("childB: wrote marker (exit %d)", code)

	// Read the marker back from childB to confirm the write worked.
	outB2, _ := forkExec(t, drv, childBID, []string{"/bin/read-marker"}, nil)
	if !strings.Contains(outB2, strings.TrimRight(markerB, "\n")) {
		t.Errorf("childB self-read: expected %q, got %q", strings.TrimRight(markerB, "\n"), outB2)
	} else {
		t.Logf("childB self-read OK: %q", strings.TrimSpace(outB2))
	}

	// Now read the same path on childA — must NOT contain childB's marker.
	outA2, codeA2 := forkExec(t, drv, childAID, []string{"/bin/read-marker"}, nil)
	if strings.Contains(outA2, strings.TrimRight(markerB, "\n")) {
		t.Errorf("FAIL (proof 3): childA sees childB's marker — disk NOT isolated.\n  childB wrote: %q\n  childA read:  %q", markerB, outA2)
	} else {
		t.Logf("PASS (proof 3): childA does not see childB's marker (exit=%d, out=%q)", codeA2, strings.TrimSpace(outA2))
	}

	// proof 4: parent disk unmodified
	// After ForkFrom + child disk writes, the parent's original ext4 file must
	// have the same mtime and size it had just before ForkFrom.
	parentStatAfter, err := os.Stat(ext4Path)
	if err != nil {
		t.Errorf("stat parent disk after child writes: %v", err)
	} else if parentStatAfter.ModTime() != parentMtimeBefore || parentStatAfter.Size() != parentSizeBefore {
		t.Errorf("FAIL (proof 4): parent disk modified.\n  before: size=%d mtime=%v\n  after:  size=%d mtime=%v",
			parentSizeBefore, parentMtimeBefore,
			parentStatAfter.Size(), parentStatAfter.ModTime())
	} else {
		t.Logf("PASS (proof 4): parent disk unmodified: size=%d mtime=%v", parentSizeBefore, parentMtimeBefore)
	}

	// summary
	t.Logf("=== ForkDiskIsolation summary ===")
	t.Logf("  Parent disk:   %s (size=%d)", ext4Path, parentSizeBefore)
	t.Logf("  Snapshot:      %s", snap.ID)
	t.Logf("  ChildA ID:     %s", childAID)
	t.Logf("  ChildA disk:   %s", childADisk)
	t.Logf("  ChildA marker: %q", strings.TrimSpace(markerA))
	t.Logf("  ChildB ID:     %s", childBID)
	t.Logf("  ChildB disk:   %s", childBDisk)
	t.Logf("  ChildB marker: %q", strings.TrimSpace(markerB))
}

//go:build integration

package cloudhypervisor

// Integration test for TakeSnapshot + ForkFrom.
//
// # What this tests
//
//   - TestSnapshotFork: boots a real microVM (kernel panic/HLT, same as
//     TestBootLifecycle), takes a snapshot with TakeSnapshot, forks one
//     child with ForkFrom, and verifies both the artifact Store record and
//     that the child VM reaches Running state.
//
// # Guard conditions
//
// The test skips (never fails) when the environment lacks:
//   - /dev/kvm
//   - the cloud-hypervisor binary (default $HOME/.local/bin/cloud-hypervisor;
//     override with CLOUD_HYPERVISOR_BIN)
//   - boot artifacts (run scripts/fetch-boot-artifacts.sh)
//
// # Running
//
//	bash scripts/fetch-boot-artifacts.sh
//	go test -tags integration ./internal/core/driver/cloudhypervisor/... \
//	    -run TestSnapshotFork -v -count=1 -timeout 180s

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

func TestSnapshotFork(t *testing.T) {
	// ------------------------------------------------------------------ guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")

	// ------------------------------------------------------------------ dirs
	// Use short base paths to stay under the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "ch-snap-")
	if err != nil {
		t.Fatalf("MkdirTemp (socketDir): %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	if len(socketDir)+35 > 107 {
		t.Skipf("skipping: socketDir too long for Unix socket: %s", socketDir)
	}

	snapDir, err := os.MkdirTemp("/tmp", "ch-snap-data-")
	if err != nil {
		t.Fatalf("MkdirTemp (snapDir): %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(snapDir) })

	// ------------------------------------------------------------------ driver
	drv, err := New(Config{
		BinaryPath:  chBin,
		SocketDir:   socketDir,
		SnapshotDir: snapDir,
		KernelPath:  kernelPath,
		VCPUs:       1,
		MemoryMiB:   128,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// ------------------------------------------------------------------ start parent VM
	parentID := domain.NewSandboxID()
	iid, err := drv.Start(ctx, driver.StartRequest{SandboxID: parentID})
	if err != nil {
		t.Fatalf("Start (parent): %v", err)
	}
	t.Logf("parent VM started, instanceID=%s", iid)
	t.Cleanup(func() { _ = drv.Stop(context.Background(), parentID) })

	obs := pollForState(t, drv, parentID, driver.Running, 30*time.Second)
	if obs.State != driver.Running {
		t.Fatalf("parent VM did not reach Running: %v (%s)", obs.State, obs.Detail)
	}

	// ------------------------------------------------------------------ snapshot
	snap, err := drv.TakeSnapshot(ctx, parentID, artifact.KindTransient)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}
	t.Logf("snapshot taken: ID=%s size=%d kind=%s", snap.ID, snap.Size, snap.Kind)

	// Parent VM should be running again after resume.
	obs = pollForState(t, drv, parentID, driver.Running, 15*time.Second)
	if obs.State != driver.Running {
		t.Errorf("parent VM not Running after snapshot: %v (%s)", obs.State, obs.Detail)
	}

	// Validate the artifact record.
	if err := snap.Validate(); err != nil {
		t.Errorf("snap.Validate: %v", err)
	}

	// The artifact.Store record must be readable (commit marker present and intact).
	stored, err := drv.snapshotStore.Read(snap.ID)
	if err != nil {
		t.Errorf("snapshotStore.Read: %v", err)
	} else if stored.ID != snap.ID {
		t.Errorf("stored.ID mismatch: got %s, want %s", stored.ID, snap.ID)
	}

	// ------------------------------------------------------------------ fork
	childID := domain.NewSandboxID()
	instanceIDs, err := drv.ForkFrom(ctx, snap, []domain.SandboxID{childID})
	if err != nil {
		t.Fatalf("ForkFrom: %v", err)
	}
	if len(instanceIDs) != 1 {
		t.Fatalf("ForkFrom returned %d instanceIDs, want 1", len(instanceIDs))
	}
	if instanceIDs[0] == "" {
		t.Error("ForkFrom returned empty instanceID for child")
	}
	t.Logf("fork child started: instanceID=%s", instanceIDs[0])
	t.Cleanup(func() { _ = drv.Stop(context.Background(), childID) })

	// Child VM should reach Running state after restore.
	obs = pollForState(t, drv, childID, driver.Running, 30*time.Second)
	if obs.State != driver.Running {
		t.Errorf("child VM did not reach Running: %v (%s)", obs.State, obs.Detail)
	}

	t.Logf("=== Snapshot/Fork summary ===")
	t.Logf("  Parent:   %s (Running)", parentID)
	t.Logf("  Snapshot: %s", snap.ID)
	t.Logf("  Child:    %s (Running)", childID)
}

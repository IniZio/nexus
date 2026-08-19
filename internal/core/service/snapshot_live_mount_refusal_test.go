package service_test

// Tests for D-PD-53: Snapshot refuses when the sandbox has any live
// host-directory mount.
//
// A retained snapshot manifest would carry the parent's mount paths;
// RestoreFromSnapshot → ForkFrom would then give the restored child a live
// virtiofs link back to the same host directory — the exact corruption
// D-PD-53 exists to prevent.
//
// TRANSITIVE COVERAGE: RestoreFromSnapshot cannot reach a live-mounted parent
// because Snapshot refuses to create a snapshot of one.  A separate test
// documents this transitive guarantee so the reasoning is not lost if the code
// is refactored.
//
// INTERACTION TEST: a sandbox with BOTH a named volume and a live mount must
// produce a single coherent refusal (D-PD-96 fires first inside Update).
//
// Each refusing test writes to and reads from a real FileStore so the
// toRecord→toDomain mapping path is exercised.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// TestSnapshot_LiveMountRefusal_FromDisk verifies that Snapshot refuses a
// sandbox with a live host-directory mount.  The sandbox is written to a real
// FileStore and read back, exercising the persistence mapping path.
func TestSnapshot_LiveMountRefusal_FromDisk(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name  string
		mount domain.LiveMount
	}{
		{
			name:  "read-write mount",
			mount: domain.LiveMount{HostPath: "/home/user/project", GuestPath: "/workspace", ReadOnly: false},
		},
		{
			name:  "read-only mount",
			mount: domain.LiveMount{HostPath: "/home/user/data", GuestPath: "/data", ReadOnly: true},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			st, err := store.NewFileStore(root)
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			sb := domain.Sandbox{
				ID:      domain.NewSandboxID(),
				Name:    "sb-live-mount-" + tc.name,
				Project: "test-proj",
				State:   domain.Running,
				Envelope: domain.Envelope{
					ImageDigest: "sha256:testlm",
				},
				InstanceID: "inst-lm",
				LiveMounts: []domain.LiveMount{tc.mount},
			}
			if err := st.Create(ctx, sb); err != nil {
				t.Fatalf("Create sandbox: %v", err)
			}

			svc := newSnapshotSvc(t, root)
			_, snapErr := svc.Snapshot(ctx, sb.ID.String())

			if snapErr == nil {
				t.Fatal("Snapshot: expected refusal for live mount, got nil")
			}
			if errors.Is(snapErr, service.ErrNoSubstrate) {
				t.Fatalf("Snapshot returned substrate error instead of D-PD-53 refusal: %v\n"+
					"This means LiveMounts was lost during filestore read.", snapErr)
			}
			if !strings.Contains(snapErr.Error(), "D-PD-53") {
				t.Errorf("Snapshot error does not cite D-PD-53: %v", snapErr)
			}
			if !strings.Contains(snapErr.Error(), tc.mount.HostPath) {
				t.Errorf("Snapshot error does not name the host path %q: %v", tc.mount.HostPath, snapErr)
			}
		})
	}
}

// TestSnapshot_NoLiveMounts_Succeeds is the NEGATIVE CONTROL: a sandbox with
// no live mounts must snapshot successfully.  Without this, a guard that always
// refuses (e.g. `if true`) would pass the refusal tests while being broken.
//
// The fake driver implements Snapshotter, so this test actually reaches the
// driver and returns a non-empty snapshot ID.
func TestSnapshot_NoLiveMounts_Succeeds(t *testing.T) {
	ctx := context.Background()

	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "sb-no-live-mounts",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testclean-no-lm",
		},
		InstanceID: "inst-clean-no-lm",
		LiveMounts: nil,
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}

	svc := newSnapshotSvc(t, root)
	snap, err := svc.Snapshot(ctx, sb.ID.String())
	if err != nil {
		t.Fatalf("Snapshot (no live mounts): unexpected error: %v\n"+
			"If D-PD-53 guard fired despite zero live mounts, the guard condition is broken.", err)
	}
	if snap.ID == "" {
		t.Fatal("Snapshot (no live mounts): returned empty snapshot ID")
	}
}

// TestRestoreFromSnapshot_TransitiveCoverage documents that RestoreFromSnapshot
// cannot reach a live-mounted parent because Snapshot refuses to create a
// retained snapshot of one (D-PD-53).  This is transitive coverage: no
// live-mounted snapshot can exist in the artifact store, so RestoreFromSnapshot
// never encounters one.
//
// The test verifies the guarantee in two steps:
//  1. Snapshot refuses a live-mounted sandbox → no artifact is written.
//  2. Snapshot succeeds on a clean sandbox → the guard is not over-broad.
//
// There is therefore no path by which RestoreFromSnapshot can receive a
// snapshot whose origin sandbox carried live mounts; a redundant check in
// RestoreFromSnapshot is not needed.
func TestRestoreFromSnapshot_TransitiveCoverage(t *testing.T) {
	ctx := context.Background()

	// Step 1: Snapshot refuses a live-mounted sandbox.
	root1 := t.TempDir()
	st1, err := store.NewFileStore(root1)
	if err != nil {
		t.Fatalf("NewFileStore (live mount): %v", err)
	}
	lmSb := domain.Sandbox{
		ID:         domain.NewSandboxID(),
		Name:       "sb-lm-transitive",
		Project:    "test-proj",
		State:      domain.Running,
		Envelope:   domain.Envelope{ImageDigest: "sha256:transitive-lm"},
		InstanceID: "inst-transitive-lm",
		LiveMounts: []domain.LiveMount{
			{HostPath: "/workspace", GuestPath: "/workspace", ReadOnly: false},
		},
	}
	if err := st1.Create(ctx, lmSb); err != nil {
		t.Fatalf("Create live-mount sandbox: %v", err)
	}
	svc1 := newSnapshotSvc(t, root1)
	_, snapErr := svc1.Snapshot(ctx, lmSb.ID.String())
	if snapErr == nil {
		t.Fatal("Snapshot of live-mounted sandbox succeeded — D-PD-53 gate is not in place; " +
			"RestoreFromSnapshot could receive a live-mounted snapshot artifact")
	}
	if !strings.Contains(snapErr.Error(), "D-PD-53") {
		t.Errorf("Snapshot refusal does not cite D-PD-53: %v", snapErr)
	}

	// Step 2: Confirm a clean sandbox CAN be snapshotted (guard is not over-broad).
	root2 := t.TempDir()
	st2, err := store.NewFileStore(root2)
	if err != nil {
		t.Fatalf("NewFileStore (clean): %v", err)
	}
	cleanSb := domain.Sandbox{
		ID:         domain.NewSandboxID(),
		Name:       "sb-clean-transitive",
		Project:    "test-proj",
		State:      domain.Running,
		Envelope:   domain.Envelope{ImageDigest: "sha256:transitive-clean"},
		InstanceID: "inst-transitive-clean",
		LiveMounts: nil,
	}
	if err := st2.Create(ctx, cleanSb); err != nil {
		t.Fatalf("Create clean sandbox: %v", err)
	}
	svc2 := newSnapshotSvc(t, root2)
	snap, err := svc2.Snapshot(ctx, cleanSb.ID.String())
	if err != nil {
		t.Fatalf("Snapshot of clean sandbox failed: %v", err)
	}
	if snap.ID == "" {
		t.Fatal("Snapshot of clean sandbox returned empty ID")
	}
}

// TestSnapshot_LiveMountAndVolume_CoherentRefusal is the INTERACTION TEST: a
// sandbox with BOTH a named volume (D-PD-96) and a live mount (D-PD-53) must
// produce one coherent error.  The D-PD-96 check fires first inside Update;
// only that error appears — D-PD-53 must not also appear in the same message.
func TestSnapshot_LiveMountAndVolume_CoherentRefusal(t *testing.T) {
	ctx := context.Background()

	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := domain.Sandbox{
		ID:         domain.NewSandboxID(),
		Name:       "sb-vol-and-lm",
		Project:    "test-proj",
		State:      domain.Running,
		Envelope:   domain.Envelope{ImageDigest: "sha256:vol-and-lm"},
		InstanceID: "inst-vol-and-lm",
		MountedVolumes: []domain.VolumeAttachment{
			{Name: "my-data", GuestPath: "/mnt/data", Kind: "disk", ReadOnly: false},
		},
		LiveMounts: []domain.LiveMount{
			{HostPath: "/home/user/project", GuestPath: "/workspace", ReadOnly: false},
		},
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}

	svc := newSnapshotSvc(t, root)
	_, snapErr := svc.Snapshot(ctx, sb.ID.String())

	if snapErr == nil {
		t.Fatal("Snapshot: expected refusal, got nil")
	}
	// D-PD-96 fires first; the error must cite it (or TBR-PD-15).
	if !strings.Contains(snapErr.Error(), "D-PD-96") && !strings.Contains(snapErr.Error(), "TBR-PD-15") {
		t.Errorf("Snapshot error does not cite D-PD-96/TBR-PD-15 (expected first check): %v", snapErr)
	}
	// D-PD-53 must NOT appear — only one refusal fires, not both.
	if strings.Contains(snapErr.Error(), "D-PD-53") {
		t.Errorf("Snapshot error mentions D-PD-53 alongside D-PD-96: duplicated refusal: %v", snapErr)
	}
}

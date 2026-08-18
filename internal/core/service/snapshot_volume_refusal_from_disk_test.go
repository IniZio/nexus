package service_test

// TestSnapshot_VolumeRefusal_FromDisk is the regression guard for the
// D-PD-96 snapshot gate: Service.Snapshot must refuse when any named volume is
// attached, because a snapshot manifest would carry the parent's volume paths,
// and RestoreFromSnapshot → ForkFrom would then point the child's config.json
// at the PARENT's volume image read-write (data corruption).
//
// The test writes the sandbox to a real FileStore and reads it back, mirroring
// TestFork_DiskVolumeRefusal_FromDisk. The from-disk path is the one that
// matters: a persistence bug already silently disabled this class of guard
// once (filestore.go dropped MountedVolumes in its record mapping), so an
// in-memory-only test is insufficient.
//
// Both volume kinds (kind=disk and kind=dir) are covered because the gate is
// kind-agnostic (D-PD-96 is uniform).
//
// The NEGATIVE CONTROL (sandbox with no volumes) is included so that a gate
// that refuses everything — i.e. if false was changed to if true — would fail
// the test.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newSnapshotSvc creates a FileStore at root and a Service wired to it,
// exercising the toRecord→toDomain mapping on every read.
func newSnapshotSvc(t *testing.T, root string) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	return service.New(st, fake.New(), lifecycle.New())
}

func TestSnapshot_VolumeRefusal_FromDisk(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		volume domain.VolumeAttachment
	}{
		{
			name:   "kind=disk",
			volume: domain.VolumeAttachment{Name: "my-data", GuestPath: "/mnt/data", Kind: "disk", ReadOnly: false},
		},
		{
			name:   "kind=dir",
			volume: domain.VolumeAttachment{Name: "my-dir", GuestPath: "/mnt/dir", Kind: "dir", ReadOnly: false},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			// Write the sandbox with a named volume to a real FileStore so the
			// mapping path (toRecord→toDomain) is exercised on read.
			st, err := store.NewFileStore(root)
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			sb := domain.Sandbox{
				ID:      domain.NewSandboxID(),
				Name:    "sb-with-vol-" + tc.name,
				Project: "test-proj",
				State:   domain.Running, // TriggerSnapshot is valid from Running (§S1)
				Envelope: domain.Envelope{
					ImageDigest: "sha256:testparent",
				},
				InstanceID:     "inst-sb",
				MountedVolumes: []domain.VolumeAttachment{tc.volume},
			}
			if err := st.Create(ctx, sb); err != nil {
				t.Fatalf("Create sandbox: %v", err)
			}

			svc := newSnapshotSvc(t, root)

			// Snapshot must be refused with the D-PD-96 / TBR-PD-15 error.
			// If MountedVolumes was lost on disk read, the gate would not fire
			// and TakeSnapshot on the fake driver would succeed instead —
			// proving the guard never ran.
			_, snapErr := svc.Snapshot(ctx, sb.ID.String())
			if snapErr == nil {
				t.Fatal("Snapshot: expected error for attached volume, got nil")
			}

			// Must be the D-PD-96 refusal, not a substrate/driver error.
			if errors.Is(snapErr, service.ErrNoSubstrate) {
				t.Fatalf("Snapshot returned substrate error instead of D-PD-96 volume refusal: %v\n"+
					"This means MountedVolumes was lost during filestore read — the bug is back.", snapErr)
			}
			if !strings.Contains(snapErr.Error(), "TBR-PD-15") && !strings.Contains(snapErr.Error(), "D-PD-96") {
				t.Fatalf("Snapshot error does not mention TBR-PD-15 or D-PD-96 — unexpected error: %v", snapErr)
			}
		})
	}
}

// TestSnapshot_NoVolumes_Succeeds is the negative control: a sandbox with no
// named volumes must snapshot successfully. Without this, a gate that refuses
// everything (e.g. if true instead of if len(attachedVolDescs) > 0) would
// pass the refusal tests while breaking the happy path.
func TestSnapshot_NoVolumes_Succeeds(t *testing.T) {
	ctx := context.Background()

	root := t.TempDir()

	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	sb := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "sb-no-vols",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testclean",
		},
		InstanceID:     "inst-clean",
		MountedVolumes: nil, // no volumes
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create sandbox: %v", err)
	}

	svc := newSnapshotSvc(t, root)

	// No volumes → gate must not fire → fake driver succeeds.
	snap, err := svc.Snapshot(ctx, sb.ID.String())
	if err != nil {
		t.Fatalf("Snapshot (no volumes): unexpected error: %v\n"+
			"If the gate refused despite zero volumes, the guard condition is broken.", err)
	}
	if snap.ID == "" {
		t.Fatal("Snapshot (no volumes): returned empty snapshot ID")
	}
}

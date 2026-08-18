package service_test

// Tests for D-PD-96: Fork refuses when the parent has ANY attached named
// volume, regardless of kind.
//
// Three cases:
//   - kind=dir volume → fork REFUSED (the kind=dir hole plugged by D-PD-96).
//   - kind=disk volume → fork still REFUSED (D-PD-95 behaviour preserved).
//   - no volumes → fork SUCCEEDS (the guard must not refuse the zero-volume case).
//
// Each refusing test READS the parent from a real FileStore so it exercises the
// toRecord→toDomain mapping path that historically dropped MountedVolumes
// silently.

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

// buildSvcWithParent writes parent to a fresh FileStore and returns a Service
// backed by a second open of the same root, so Fork reads from disk.
func buildSvcWithParent(t *testing.T, parent domain.Sandbox) *service.Service {
	t.Helper()
	ctx := context.Background()

	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := st.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	st2, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	return service.New(st2, fake.New(), lifecycle.New())
}

// assertVolumeRefusal checks that forkErr is the D-PD-96 interim gate and not
// a substrate/driver error (which would mean MountedVolumes was lost on read).
func assertVolumeRefusal(t *testing.T, forkErr error, label string) {
	t.Helper()
	if forkErr == nil {
		t.Fatalf("%s: Fork: expected refusal, got nil", label)
	}
	if strings.Contains(forkErr.Error(), "not snapshotable") ||
		errors.Is(forkErr, service.ErrNoSubstrate) {
		t.Fatalf("%s: Fork returned a substrate error instead of D-PD-96 volume refusal: %v\n"+
			"This means MountedVolumes was lost during filestore read — the mapping bug is back.", label, forkErr)
	}
	if !strings.Contains(forkErr.Error(), "TBR-PD-15") && !strings.Contains(forkErr.Error(), "D-PD-96") {
		t.Fatalf("%s: Fork error does not mention TBR-PD-15 or D-PD-96 — unexpected error: %v", label, forkErr)
	}
}

// TestFork_DirVolumeRefusal_FromDisk covers the kind=dir hole (D-PD-96).
// A kind=dir volume is a host directory served over virtiofs; forking gives two
// VMs a shared mutable view of the same host directory, defeating fork isolation
// (D-PD-53).  The parent is written to and re-read from a real FileStore so
// the mapping path is exercised.
func TestFork_DirVolumeRefusal_FromDisk(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-with-dir-vol",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-dir",
		},
		InstanceID: "inst-parent-dir",
		MountedVolumes: []domain.VolumeAttachment{
			{Name: "my-src", GuestPath: "/mnt/src", Kind: "dir", ReadOnly: false},
		},
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)
	assertVolumeRefusal(t, forkErr, "kind=dir")

	// The error must identify the offending volume and its kind.
	if !strings.Contains(forkErr.Error(), "my-src") {
		t.Errorf("Fork error does not name the offending volume: %v", forkErr)
	}
	if !strings.Contains(forkErr.Error(), "kind=dir") {
		t.Errorf("Fork error does not mention the volume kind: %v", forkErr)
	}
}

// TestFork_DiskVolumeRefusal_UniformCheck confirms kind=disk is still refused
// under the widened (D-PD-96) check, and that the error names the volume and
// its kind.  The parent is written to and re-read from a real FileStore.
func TestFork_DiskVolumeRefusal_UniformCheck(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-with-disk-vol-dpd96",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-disk96",
		},
		InstanceID: "inst-parent-disk96",
		MountedVolumes: []domain.VolumeAttachment{
			{Name: "my-data", GuestPath: "/mnt/data", Kind: "disk", ReadOnly: false},
		},
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)
	assertVolumeRefusal(t, forkErr, "kind=disk")

	if !strings.Contains(forkErr.Error(), "my-data") {
		t.Errorf("Fork error does not name the offending volume: %v", forkErr)
	}
	if !strings.Contains(forkErr.Error(), "kind=disk") {
		t.Errorf("Fork error does not mention the volume kind: %v", forkErr)
	}
}

// TestFork_NoVolumes_Succeeds asserts that a parent with zero attached volumes
// is NOT refused by the volume guard.  A guard that always refuses would pass
// the two refusal tests above while being completely broken.
//
// The fake driver does not implement Snapshotter/Forker, so Fork will fail with
// ErrNoSubstrate — but that is a driver-capability error, not the volume guard.
// We accept any error that is NOT the volume guard refusal (TBR-PD-15/D-PD-96).
func TestFork_NoVolumes_Succeeds(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-no-vols",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-novol",
		},
		InstanceID:     "inst-parent-novol",
		MountedVolumes: nil,
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)

	// The volume guard must NOT fire.
	if forkErr != nil {
		if strings.Contains(forkErr.Error(), "TBR-PD-15") || strings.Contains(forkErr.Error(), "D-PD-96") {
			t.Fatalf("Fork refused a parent with NO volumes with the volume-guard error: %v", forkErr)
		}
		// Any other error (e.g. ErrNoSubstrate from the fake driver) is expected
		// and acceptable — the guard did not fire.
		if !errors.Is(forkErr, service.ErrNoSubstrate) {
			t.Logf("Fork returned non-guard error (expected with fake driver): %v", forkErr)
		}
	}
}

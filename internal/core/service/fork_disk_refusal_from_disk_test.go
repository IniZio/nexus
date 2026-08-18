package service_test

// TestFork_DiskVolumeRefusal_FromDisk is the regression guard for the
// silent-disable bug described in SD2-PERSIST: when MountedVolumes was missing
// from the filestore toRecord/toDomain mapping, the D-PD-96 fork refusal in
// Service.Fork read a parent with zero mounted volumes from disk and therefore
// never fired — even though the sandbox had been created with a kind=disk
// volume.
//
// This test writes the parent to a real FileStore, then calls Fork so the
// service READS the parent back from disk.  The in-memory path (parent never
// persisted) is insufficient because it cannot catch the mapping bug.

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

func TestFork_DiskVolumeRefusal_FromDisk(t *testing.T) {
	ctx := context.Background()

	// Write the parent sandbox to a real FileStore so the mapping path is
	// exercised on read.
	root := t.TempDir()
	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-with-disk-vol",
		Project: "test-proj",
		State:   domain.Running, // Running allows TriggerSnapshot (lifecycle table §S1)
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent",
		},
		InstanceID: "inst-parent",
		MountedVolumes: []domain.VolumeAttachment{
			// kind=disk is the attachment type that D-PD-95 refuses.
			{Name: "my-data", GuestPath: "/mnt/data", Kind: "disk", ReadOnly: false},
		},
	}
	if err := st.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	// Build the service with the same FileStore root.  Fork will resolve the
	// parent by reading from disk, exercising toRecord→toDomain.
	st2, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	svc := service.New(st2, fake.New(), lifecycle.New())

	// Fork must be refused with the D-PD-96 error.  If MountedVolumes was
	// lost on disk read, Fork would instead fail with a driver capability
	// error (fake driver does not support fork/snapshot), proving the guard
	// never ran.
	_, forkErr := svc.Fork(ctx, parent.ID.String(), 1)
	if forkErr == nil {
		t.Fatal("Fork: expected error for kind=disk volume, got nil")
	}

	// The error must be the D-PD-96 refusal, not a substrate/driver error.
	// Check for the canonical marker strings from service.go.
	if strings.Contains(forkErr.Error(), "not snapshotable") ||
		errors.Is(forkErr, service.ErrNoSubstrate) {
		t.Fatalf("Fork returned a substrate error instead of D-PD-96 volume refusal: %v\n"+
			"This means MountedVolumes was lost during filestore read — the bug is back.", forkErr)
	}
	if !strings.Contains(forkErr.Error(), "TBR-PD-15") && !strings.Contains(forkErr.Error(), "D-PD-96") {
		t.Fatalf("Fork error does not mention TBR-PD-15 or D-PD-96 — unexpected error: %v", forkErr)
	}
}

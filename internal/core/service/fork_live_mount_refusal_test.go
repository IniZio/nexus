package service_test

// Tests for D-PD-53: Fork refuses when the parent has ANY live host-directory
// mount, regardless of read-only flag.
//
// Two VMs sharing one host worktree read-write is the exact corruption scenario
// D-PD-53 exists to prevent.  The check must fire after the D-PD-96 named-volume
// check (line order in service.go), so interaction tests use a sandbox that has
// BOTH a named volume and a live mount to verify the first check wins and the
// message is coherent (not duplicated or contradictory).
//
// Each refusing test READS the parent from a real FileStore so it exercises the
// toRecord→toDomain mapping path that historically dropped fields silently.

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestFork_LiveMountRefusal_ReadWrite covers the primary D-PD-53 case: a
// read-write virtiofs share.  The parent is written to and re-read from a real
// FileStore so the persistence path is exercised.
func TestFork_LiveMountRefusal_ReadWrite(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-live-mount-rw",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-lm-rw",
		},
		InstanceID: "inst-parent-lm-rw",
		LiveMounts: []domain.LiveMount{
			{HostPath: "/home/user/project", GuestPath: "/workspace", ReadOnly: false},
		},
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)

	if forkErr == nil {
		t.Fatal("Fork: expected refusal for live mount, got nil")
	}
	if !strings.Contains(forkErr.Error(), "D-PD-53") {
		t.Errorf("Fork error does not cite D-PD-53: %v", forkErr)
	}
	if !strings.Contains(forkErr.Error(), "/home/user/project") {
		t.Errorf("Fork error does not name the host path: %v", forkErr)
	}
	if !strings.Contains(forkErr.Error(), "/workspace") {
		t.Errorf("Fork error does not name the guest path: %v", forkErr)
	}
}

// TestFork_LiveMountRefusal_ReadOnly confirms the guard fires even when the
// mount is read-only.  A read-only virtiofs share still exposes the host
// directory to both child VMs; the corruption concern is on the host side
// (concurrent writers on the host), not the guest-side flag.
func TestFork_LiveMountRefusal_ReadOnly(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-live-mount-ro",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-lm-ro",
		},
		InstanceID: "inst-parent-lm-ro",
		LiveMounts: []domain.LiveMount{
			{HostPath: "/home/user/data", GuestPath: "/data", ReadOnly: true},
		},
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)

	if forkErr == nil {
		t.Fatal("Fork: expected refusal for read-only live mount, got nil")
	}
	if !strings.Contains(forkErr.Error(), "D-PD-53") {
		t.Errorf("Fork error does not cite D-PD-53: %v", forkErr)
	}
}

// TestFork_NoLiveMounts_Succeeds is the NEGATIVE CONTROL: a parent with no live
// mounts must NOT be refused by the D-PD-53 guard.  The fake driver does not
// implement Snapshotter/Forker, so Fork fails with ErrNoSubstrate — but that is
// a driver-capability error, not the live-mount guard.
func TestFork_NoLiveMounts_Succeeds(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-no-live-mounts",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-no-lm",
		},
		InstanceID: "inst-parent-no-lm",
		LiveMounts: nil,
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)

	// The D-PD-53 guard must NOT fire.
	if forkErr != nil && strings.Contains(forkErr.Error(), "D-PD-53") {
		t.Fatalf("Fork refused a parent with NO live mounts with D-PD-53 error: %v", forkErr)
	}
	// Any other error (ErrNoSubstrate from fake driver) is expected and acceptable.
}

// TestFork_LiveMountAndVolume_CoherentRefusal is the INTERACTION TEST: a
// sandbox that has both a named volume (D-PD-96) and a live mount (D-PD-53)
// must produce a single coherent refusal.  The D-PD-96 check fires first
// (order in service.go); the error must mention D-PD-96 or TBR-PD-15, and
// must NOT mention D-PD-53 (only the first check fires).
func TestFork_LiveMountAndVolume_CoherentRefusal(t *testing.T) {
	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "parent-vol-and-lm",
		Project: "test-proj",
		State:   domain.Running,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:testparent-vol-lm",
		},
		InstanceID: "inst-parent-vol-lm",
		MountedVolumes: []domain.VolumeAttachment{
			{Name: "my-data", GuestPath: "/mnt/data", Kind: "disk", ReadOnly: false},
		},
		LiveMounts: []domain.LiveMount{
			{HostPath: "/home/user/project", GuestPath: "/workspace", ReadOnly: false},
		},
	}

	svc := buildSvcWithParent(t, parent)
	_, forkErr := svc.Fork(context.Background(), parent.ID.String(), 1)

	if forkErr == nil {
		t.Fatal("Fork: expected refusal, got nil")
	}
	// D-PD-96 fires first; message must cite it (or TBR-PD-15).
	if !strings.Contains(forkErr.Error(), "D-PD-96") && !strings.Contains(forkErr.Error(), "TBR-PD-15") {
		t.Errorf("Fork error does not cite D-PD-96/TBR-PD-15 (expected first check): %v", forkErr)
	}
	// D-PD-53 must NOT appear — only one refusal fires, not both.
	if strings.Contains(forkErr.Error(), "D-PD-53") {
		t.Errorf("Fork error mentions D-PD-53 alongside D-PD-96: duplicated/confusing refusal: %v", forkErr)
	}
}

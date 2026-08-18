package service_test

// start_volume_guard_test.go — D-PD-94: Service.Start volume guard through the
// production path.
//
// TestService_Start_VolumeGuard_ProductionPath proves that the `if s.volumes != nil`
// block inside Service.Start is reached and blocks the driver call when a
// conflicting running holder exists.  Unlike TestCheckRWAttach_startGuard, this
// test calls Service.Start; mutating the guard block to `if false && s.volumes != nil`
// MUST make this test fail (the driver's Start would be called instead of erroring).

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// TestService_Start_VolumeGuard_ProductionPath drives the M-b start-time guard
// through Service.Start itself.  It does NOT call checkRWAttach directly.
//
// Setup:
//   - Sandbox A: Stopped, has a rw kind=disk MountedVolumes entry for "guard-vol".
//   - Sandbox B: Running, holds "guard-vol" in the volumestore.
//
// Expectation: svc.Start(A) returns an error containing "start volume guard"
// and the fake driver's Start method is never invoked.
func TestService_Start_VolumeGuard_ProductionPath(t *testing.T) {
	ctx := context.Background()

	st := newTestStore(t)
	vs := newVolumeStore(t)
	diskDir := t.TempDir()

	fd := fake.New()
	svc := service.New(st, fd, lifecycle.New()).
		WithVolumes(vs).
		WithDiskDir(diskDir)

	// Create the volume in the store.
	volName := "guard-vol"
	if _, err := vs.Create(ctx, volName, volumestore.KindDisk, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	// Sandbox A: Stopped, with a rw kind=disk attachment for "guard-vol".
	sbA := makeSandbox("a", "test", domain.Stopped)
	sbA.MountedVolumes = []domain.VolumeAttachment{
		{
			Name:      volName,
			GuestPath: "/mnt/data",
			Kind:      string(volumestore.KindDisk),
			ReadOnly:  false,
		},
	}
	if err := st.Create(ctx, sbA); err != nil {
		t.Fatalf("st.Create A: %v", err)
	}

	// Sandbox B: Running, same volume — it is the active holder.
	sbB := makeSandbox("b", "test", domain.Running)
	sbB.MountedVolumes = []domain.VolumeAttachment{
		{
			Name:      volName,
			GuestPath: "/mnt/data",
			Kind:      string(volumestore.KindDisk),
			ReadOnly:  false,
		},
	}
	if err := st.Create(ctx, sbB); err != nil {
		t.Fatalf("st.Create B: %v", err)
	}
	// Attach B to the volume in the volumestore so the guard sees a running holder.
	if err := vs.AttachAndPrune(volName, sbB.ID.String(), nil); err != nil {
		t.Fatalf("vs.AttachAndPrune B: %v", err)
	}

	// Start A — the guard must fire and return an error.
	_, err := svc.Start(ctx, sbA.ID.String())
	if err == nil {
		t.Fatal("svc.Start(A): expected error from volume guard, got nil")
	}
	if !strings.Contains(err.Error(), "start volume guard") {
		t.Fatalf("svc.Start(A): error missing 'start volume guard': %v", err)
	}

	// The driver's Start must NOT have been called.
	for _, c := range fd.Calls() {
		if c.Kind == fake.CallStart {
			t.Fatalf("svc.Start(A): driver.Start was called despite volume guard error")
		}
	}
}

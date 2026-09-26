package supervisor

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
)

// TestProbeAndSeedGuest_OverlayBeforeUserMounts asserts overlay seed precedes user-mount seed.
func TestProbeAndSeedGuest_OverlayBeforeUserMounts(t *testing.T) {
	var mu sync.Mutex
	var events []string

	origOvl := seedOverlayClaudeConfigFn
	seedOverlayClaudeConfigFn = func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		mu.Lock()
		events = append(events, "overlay")
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { seedOverlayClaudeConfigFn = origOvl })

	origUM := seedUserMountsFn
	seedUserMountsFn = func(_ context.Context, _ domain.SandboxID, _ service.UserMountManifest, _ service.GuestExecer) error {
		mu.Lock()
		events = append(events, "usermount")
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { seedUserMountsFn = origUM })

	manifest := service.UserMountManifest{}
	noopExecer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, _ []string, _ io.Reader) (int32, error) {
		return 0, nil
	})
	in := guestSeedInputs{
		AgentCfgLowerGuestPath: "/fake-lower",
		UserMounts:             &manifest,
		ProfileSeeder:          noopSeeder,
		GitSeeder:              noopSeeder,
		CredentialHelperSeeder: noopSeeder,
		Execer:                 noopExecer,
	}

	if err := probeAndSeedGuest(context.Background(), pingOKProber{}, in); err != nil {
		t.Fatalf("probeAndSeedGuest returned unexpected error: %v", err)
	}

	overlayIdx := -1
	usermountIdx := -1
	for i, ev := range events {
		switch ev {
		case "overlay":
			overlayIdx = i
		case "usermount":
			usermountIdx = i
		}
	}
	if overlayIdx == -1 {
		t.Error("seedOverlayClaudeConfigFn was not called")
	}
	if usermountIdx == -1 {
		t.Error("seedUserMountsFn was not called")
	}
	if overlayIdx != -1 && usermountIdx != -1 && overlayIdx >= usermountIdx {
		t.Errorf("ordering violation: overlay event index %d >= usermount event index %d; overlay must precede usermount", overlayIdx, usermountIdx)
	}
}

// TestProbeAndSeedGuest_OverlayFailure_UserMountsNotCalledFirst verifies overlay failure aborts user-mount seed.
func TestProbeAndSeedGuest_OverlayFailure_UserMountsNotCalledFirst(t *testing.T) {
	stubErr := errors.New("branch3: test-injected overlay failure")

	origOvl := seedOverlayClaudeConfigFn
	seedOverlayClaudeConfigFn = func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		return stubErr
	}
	t.Cleanup(func() { seedOverlayClaudeConfigFn = origOvl })

	userMountCalled := false
	origUM := seedUserMountsFn
	seedUserMountsFn = func(_ context.Context, _ domain.SandboxID, _ service.UserMountManifest, _ service.GuestExecer) error {
		userMountCalled = true
		return nil
	}
	t.Cleanup(func() { seedUserMountsFn = origUM })

	manifest := service.UserMountManifest{}
	noopExecer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, _ []string, _ io.Reader) (int32, error) {
		return 0, nil
	})
	in := guestSeedInputs{
		AgentCfgLowerGuestPath: "/fake-lower",
		UserMounts:             &manifest,
		ProfileSeeder:          noopSeeder,
		GitSeeder:              noopSeeder,
		CredentialHelperSeeder: noopSeeder,
		Execer:                 noopExecer,
	}

	err := probeAndSeedGuest(context.Background(), pingOKProber{}, in)
	if err == nil {
		t.Error("expected non-nil error when overlay seed fails (fail-closed); got nil")
	}
	if userMountCalled {
		t.Error("seedUserMountsFn was called after a fatal overlay failure; it must not run when the overlay seed aborts boot")
	}
}

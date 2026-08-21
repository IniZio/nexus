package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

// TestWireLiveMountsToConfig_SetsVirtiofsdPath verifies that the production
// wiring function (wireLiveMountsToConfig) sets Config.VirtiofsdPath to a
// non-empty value when live mounts are present.
//
// Mutation signal: deleting the `cfg.VirtiofsdPath = vp` line inside
// wireLiveMountsToConfig causes this test to fail with:
//
//	VirtiofsdPath should be non-empty after wiring, got ""
//
// No compile error is produced by that mutation because `vp` is also the
// function's return value (`return vp, nil`), so the variable remains used
// even without the cfg assignment. The build stays clean and only the
// behavioural assertion below detects the drop.
func TestWireLiveMountsToConfig_SetsVirtiofsdPath(t *testing.T) {
	// Point NEXUS3_VIRTIOFSD_PATH at a real executable so resolveVirtiofsdPath
	// succeeds without requiring virtiofsd to be installed on the test host.
	fake := filepath.Join(t.TempDir(), "virtiofsd")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_VIRTIOFSD_PATH", fake)

	cfg := cloudhypervisor.Config{}
	mounts := []domain.LiveMount{{HostPath: "/tmp/host", GuestPath: "/work"}}

	vp, err := wireLiveMountsToConfig(&cfg, mounts)
	if err != nil {
		t.Fatalf("wireLiveMountsToConfig: unexpected error: %v", err)
	}
	if cfg.VirtiofsdPath == "" {
		t.Errorf("VirtiofsdPath should be non-empty after wiring, got %q", cfg.VirtiofsdPath)
	}
	if vp == "" {
		t.Errorf("returned virtiofsdPath should be non-empty, got %q", vp)
	}
	if cfg.VirtiofsdPath != fake {
		t.Errorf("VirtiofsdPath = %q, want %q", cfg.VirtiofsdPath, fake)
	}
	if len(cfg.LiveMounts) != 1 {
		t.Errorf("LiveMounts len = %d, want 1", len(cfg.LiveMounts))
	}
}

// TestWireLiveMountsToConfig_NoMounts_LeavesVirtiofsdPathEmpty verifies that
// wireLiveMountsToConfig does NOT set VirtiofsdPath when no mounts are
// requested — a host without virtiofsd must not be affected.
func TestWireLiveMountsToConfig_NoMounts_LeavesVirtiofsdPathEmpty(t *testing.T) {
	// Even if the env var is set, no mounts means no VirtiofsdPath resolution.
	t.Setenv("NEXUS3_VIRTIOFSD_PATH", "/this/should/not/be/read")

	cfg := cloudhypervisor.Config{}
	vp, err := wireLiveMountsToConfig(&cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VirtiofsdPath != "" {
		t.Errorf("VirtiofsdPath should be empty when no mounts given, got %q", cfg.VirtiofsdPath)
	}
	if vp != "" {
		t.Errorf("returned path should be empty when no mounts given, got %q", vp)
	}
}

// TestWireLiveMountsToConfig_MountsWithAbsentVirtiofsd_ReturnsActionableError
// verifies that when mounts are requested but virtiofsd cannot be resolved,
// the error names NEXUS3_VIRTIOFSD_PATH so the operator can act without
// reading source.
func TestWireLiveMountsToConfig_MountsWithAbsentVirtiofsd_ReturnsActionableError(t *testing.T) {
	t.Setenv("NEXUS3_VIRTIOFSD_PATH", "/nonexistent/virtiofsd")

	cfg := cloudhypervisor.Config{}
	mounts := []domain.LiveMount{{HostPath: "/tmp/host", GuestPath: "/work"}}
	_, err := wireLiveMountsToConfig(&cfg, mounts)
	if err == nil {
		t.Fatal("expected error when virtiofsd is absent, got nil")
	}
	if !strings.Contains(err.Error(), "NEXUS3_VIRTIOFSD_PATH") {
		t.Errorf("error should mention NEXUS3_VIRTIOFSD_PATH: %v", err)
	}
}

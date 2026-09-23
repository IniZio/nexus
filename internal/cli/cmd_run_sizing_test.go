package cli

import (
	"fmt"
	"testing"
)

func mibCeil(n uint32) string { return fmt.Sprintf(" --mem-ceiling=%d", int64(n)*1024*1024) }

func TestResolveRunSizingPinAtBoot(t *testing.T) {
	r, err := resolveRunSizing(512, 0, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.DriverMemoryMaxMiB != 0 {
		t.Errorf("DriverMemoryMaxMiB: got %d, want 0", r.DriverMemoryMaxMiB)
	}
	if r.PID1Args != mibCeil(512) {
		t.Errorf("PID1Args: got %q, want %q", r.PID1Args, mibCeil(512))
	}
}

func TestResolveRunSizingNoFlags(t *testing.T) {
	r, err := resolveRunSizing(0, 0, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.DriverMemoryMaxMiB != 4096 {
		t.Errorf("DriverMemoryMaxMiB: got %d, want 4096", r.DriverMemoryMaxMiB)
	}
	if r.PID1Args != mibCeil(4096) {
		t.Errorf("PID1Args: got %q, want %q", r.PID1Args, mibCeil(4096))
	}
}

func TestResolveRunSizingExplicitCeiling(t *testing.T) {
	r, err := resolveRunSizing(512, 2048, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.DriverMemoryMaxMiB != 2048 {
		t.Errorf("DriverMemoryMaxMiB: got %d, want 2048", r.DriverMemoryMaxMiB)
	}
	if r.PID1Args != mibCeil(2048) {
		t.Errorf("PID1Args: got %q, want %q", r.PID1Args, mibCeil(2048))
	}
}

func TestResolveRunSizingMemMaxBelowBootError(t *testing.T) {
	_, err := resolveRunSizing(512, 256, 0, 0)
	if err == nil {
		t.Fatal("expected error for memory-max < memory, got nil")
	}
}

func TestResolveRunSizingVCPUsMaxBelowBootError(t *testing.T) {
	_, err := resolveRunSizing(0, 0, 4, 2)
	if err == nil {
		t.Fatal("expected error for vcpus-max < vcpus, got nil")
	}
}

func TestResolveRunSizingSeamEqualCeiling(t *testing.T) {
	r, err := resolveRunSizing(512, 512, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.DriverMemoryMaxMiB != 0 {
		t.Errorf("DriverMemoryMaxMiB: got %d, want 0", r.DriverMemoryMaxMiB)
	}
}

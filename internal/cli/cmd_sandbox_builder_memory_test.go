package cli

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
)

// TestParseSandboxCreateArgs_BuilderMemory verifies that --builder-memory is
// parsed and stored correctly, and that the floor guard (< 1024 MiB) fires.
func TestParseSandboxCreateArgs_BuilderMemory(t *testing.T) {
	t.Run("explicit 4096", func(t *testing.T) {
		f, err := parseSandboxCreateArgs([]string{"--builder-memory", "4096"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.builderMemoryMiB != 4096 {
			t.Errorf("builderMemoryMiB = %d, want 4096", f.builderMemoryMiB)
		}
	})

	t.Run("omitted yields zero", func(t *testing.T) {
		f, err := parseSandboxCreateArgs([]string{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.builderMemoryMiB != 0 {
			t.Errorf("builderMemoryMiB = %d, want 0 (so builder.MemMiB() yields default)", f.builderMemoryMiB)
		}
		// Verify the default substitution happens correctly.
		spec := builder.BuilderVMSpec{MemoryMiB: uint16(f.builderMemoryMiB)}
		if got := builder.MemMiB(spec); got != builder.DefaultBuilderMemMiB {
			t.Errorf("builder.MemMiB(zero spec) = %d, want %d (default)", got, builder.DefaultBuilderMemMiB)
		}
	})

	t.Run("below floor rejected", func(t *testing.T) {
		_, err := parseSandboxCreateArgs([]string{"--builder-memory", "512"})
		if err == nil {
			t.Fatal("expected error for value below 1024 MiB floor, got nil")
		}
	})

	t.Run("exact floor accepted", func(t *testing.T) {
		f, err := parseSandboxCreateArgs([]string{"--builder-memory", "1024"})
		if err != nil {
			t.Fatalf("unexpected error for floor value: %v", err)
		}
		if f.builderMemoryMiB != 1024 {
			t.Errorf("builderMemoryMiB = %d, want 1024", f.builderMemoryMiB)
		}
	})

	t.Run("zero treated as unset (no error)", func(t *testing.T) {
		f, err := parseSandboxCreateArgs([]string{"--builder-memory", "0"})
		if err != nil {
			t.Fatalf("unexpected error for zero value: %v", err)
		}
		if f.builderMemoryMiB != 0 {
			t.Errorf("builderMemoryMiB = %d, want 0", f.builderMemoryMiB)
		}
	})
}

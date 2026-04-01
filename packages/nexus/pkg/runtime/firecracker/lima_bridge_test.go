package firecracker

import (
	"reflect"
	"testing"
)

func TestLimaBridge_WrapsVmctlOnDarwin(t *testing.T) {
	b := NewLimaBridge("nexus-firecracker")
	cmd, args := b.Wrap("vmctl-firecracker", "pause", "--id", "ws-1")

	if cmd != "limactl" {
		t.Fatalf("expected limactl, got %q", cmd)
	}
	want := []string{"shell", "nexus-firecracker", "vmctl-firecracker", "pause", "--id", "ws-1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("got %v want %v", args, want)
	}
}

func TestLimaBridge_UsesDefaultInstanceWhenEmpty(t *testing.T) {
	b := NewLimaBridge("")
	_, args := b.Wrap("vmctl-firecracker", "start", "--id", "ws-1")
	if args[1] != "nexus-firecracker" {
		t.Fatalf("expected default instance nexus-firecracker, got %q", args[1])
	}
}

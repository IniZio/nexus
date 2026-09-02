package cli

// The boot cmdline is built once by `nexus3 sandbox create` and again by the
// detached supervisor that re-boots the same VM. Those were separate copies of
// the same assembly logic; a difference between them brings the VM back missing
// its mounts or its hostname, and nothing fails loudly when it happens.
//
// These tests pin the assembled shape, and pin that the supervisor's
// reconstruction is byte-identical to what create produced.

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
)

func TestGuestBootCmdline_NoMounts(t *testing.T) {
	t.Parallel()
	got := guestBootCmdline(nil, " --auto-resize", "proj/name", -1)

	if !strings.HasPrefix(got, diskBootCmdlineBase+" --") {
		t.Errorf("cmdline must start with the base boot args; got %q", got)
	}
	if strings.Contains(got, "--workspace-mount=") {
		t.Errorf("no mounts were passed but the cmdline carries one; got %q", got)
	}
	if !strings.HasSuffix(got, " --sandbox-handle=proj-name") {
		t.Errorf("cmdline must end with the sandbox handle; got %q", got)
	}
}

func TestGuestBootCmdline_WithMounts(t *testing.T) {
	t.Parallel()
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace", FSType: "ext4", IsWorkspace: true},
		{Device: "/dev/vdc", Target: "/cache", FSType: "ext4", ReadOnly: true},
	}
	got := guestBootCmdline(mounts, " --auto-resize", "proj/name", -1)

	if n := strings.Count(got, "--workspace-mount="); n != len(mounts) {
		t.Errorf("expected one --workspace-mount token per mount (%d), got %d in %q", len(mounts), n, got)
	}
	// Order matters: the guest agent maps mounts positionally onto devices.
	wsIdx := strings.Index(got, "/dev/vdb")
	cacheIdx := strings.Index(got, "/dev/vdc")
	if wsIdx < 0 || cacheIdx < 0 || wsIdx > cacheIdx {
		t.Errorf("mounts must appear in the order given; got %q", got)
	}
	if !strings.HasSuffix(got, " --sandbox-handle=proj-name") {
		t.Errorf("cmdline must end with the sandbox handle; got %q", got)
	}
}

// The supervisor reconstructs the cmdline from the sandbox record when it
// re-boots the VM. It must reproduce exactly what the create path built, or the
// VM comes back different from the one the user created.
func TestGuestBootCmdline_SupervisorReconstructionMatchesCreate(t *testing.T) {
	t.Parallel()
	const handle = "orca/motive-42"
	mounts := []agent.GuestMount{WorkspaceGuestMount("/workspace", 0)}

	// Derive the PID-1 args exactly as buildOrcaSpawnConfig does, so this test
	// compares the assembly and not the auto-resize defaults.
	bounds := vmcfg.Resolve(vmcfg.Config{}).Bounds
	memMaxMiB := uint32(bounds.MemMaxBytes / (1024 * 1024)) //nolint:gosec // bytes→MiB, fits uint32
	pid1Args := vmcfg.Resolve(vmcfg.Config{MemMaxMiB: memMaxMiB}).PID1Args

	create := guestBootCmdline(mounts, pid1Args, handle, -1)

	cfg := buildOrcaSpawnConfig(
		"01J0SPAWN", handle, t.TempDir(), t.TempDir(), "", "", "/k", "/d",
		nil, bounds, 1, true, 0, "", "/workspace",
	)

	if cfg.Cmdline != create {
		t.Errorf("supervisor cmdline differs from the create-path cmdline:\n supervisor: %q\n     create: %q", cfg.Cmdline, create)
	}
}

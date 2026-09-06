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
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
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

	// Two extra disks: workspace at index 0, scratch at index 1 (always last).
	// The scratch index is len(extraDiskPaths)-1 = 1, not a literal.
	extraDiskPaths := []string{"/fake/workspace.img", "/fake/scratch.img"}
	scratchIdx := len(extraDiskPaths) - 1
	create := guestBootCmdline(mounts, pid1Args, handle, scratchIdx)

	cfg := buildOrcaSpawnConfig(
		"01J0SPAWN", handle, t.TempDir(), t.TempDir(), "", "", "/k", "/d",
		extraDiskPaths, bounds, 1, true, 0, "", "/workspace",
		true, // hasScratchDisk: workspace present, NoScratchDisk not set
	)

	if cfg.Cmdline != create {
		t.Errorf("supervisor cmdline differs from the create-path cmdline:\n supervisor: %q\n     create: %q", cfg.Cmdline, create)
	}
}

// TestGuestBootCmdline_NoScratchDisk verifies that a sandbox with workspace
// mounts but no scratch disk emits NO --scratch-disk= token. This is the
// dangerous absent case: if the guard incorrectly fires (e.g. on GuestMounts
// length), mkfs.ext4 -F would run against the last named volume at every boot.
//
// Covers: NoScratchDisk=true sandboxes and non-workspace sandboxes with mounts.
func TestGuestBootCmdline_NoScratchDisk(t *testing.T) {
	t.Parallel()
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace", FSType: "ext4", IsWorkspace: true},
		{Device: "/dev/vdc", Target: "/gocache", FSType: "ext4"},
	}

	// scratchIdx=-1: HasScratchDisk=false (e.g. NoScratchDisk=true was set).
	// Even though mounts is non-empty, --scratch-disk= MUST be absent.
	got := guestBootCmdline(mounts, " --auto-resize", "proj/name", -1)
	if strings.Contains(got, "--scratch-disk=") {
		t.Errorf("no-scratch sandbox must not emit --scratch-disk=; got %q", got)
	}
	// Mounts themselves must still appear (regression guard).
	if n := strings.Count(got, "--workspace-mount="); n != len(mounts) {
		t.Errorf("expected %d --workspace-mount tokens, got %d in %q", len(mounts), n, got)
	}
}

// TestGuestBootCmdline_NoScratchDisk_OrcaPath tests that buildOrcaSpawnConfig
// emits no --scratch-disk= when hasScratchDisk=false even though hasWorkspaceDisk=true.
// This directly covers the NoScratchDisk=true orca path (the destructive case
// identified in the coordinator review: hasWorkspaceDisk is NOT a safe proxy).
func TestGuestBootCmdline_NoScratchDisk_OrcaPath(t *testing.T) {
	t.Parallel()
	bounds := vmcfg.Resolve(vmcfg.Config{}).Bounds
	// Two disks: workspace only; no scratch disk (NoScratchDisk=true scenario).
	extraDiskPaths := []string{"/fake/workspace.img"}

	cfg := buildOrcaSpawnConfig(
		"01J0NOSCRATCH", "proj/noscratch", t.TempDir(), t.TempDir(), "", "", "/k", "/d",
		extraDiskPaths, bounds, 1,
		true,  // hasWorkspaceDisk=true
		0,     // workspaceDiskIndex
		"", "/workspace",
		false, // hasScratchDisk=false — explicitly not attached
	)
	if strings.Contains(cfg.Cmdline, "--scratch-disk=") {
		t.Errorf("no-scratch orca sandbox must not emit --scratch-disk=; got %q", cfg.Cmdline)
	}
}

// TestScratchDiskCmdlineArg_PresenceAbsence verifies that --scratch-disk= appears
// in the assembled cmdline exactly when a scratch disk is attached, and that the
// device path matches the ExtraDisks index → /dev/vd{b+idx} formula.
func TestScratchDiskCmdlineArg_PresenceAbsence(t *testing.T) {
	t.Parallel()

	// Case 1: scratch disk present at ExtraDisks index 1 → /dev/vdc (b+1=c)
	withScratch := guestBootCmdline(
		[]agent.GuestMount{{Device: "/dev/vdb", Target: "/workspace", FSType: "ext4", IsWorkspace: true}},
		" --auto-resize", "proj/name", 1,
	)
	if !strings.Contains(withScratch, "--scratch-disk=/dev/vdc") {
		t.Errorf("scratch disk at idx=1 must emit --scratch-disk=/dev/vdc; got %q", withScratch)
	}

	// Case 2: 5 extra disks (indices 0-4); scratch at index 4 → /dev/vdf (b+4=f)
	withScratchF := guestBootCmdline(nil, " --auto-resize", "proj/name", 4)
	if !strings.Contains(withScratchF, "--scratch-disk=/dev/vdf") {
		t.Errorf("scratch disk at idx=4 must emit --scratch-disk=/dev/vdf; got %q", withScratchF)
	}

	// Case 3: no scratch disk (idx < 0) → --scratch-disk= must be absent
	withoutScratch := guestBootCmdline(nil, " --auto-resize", "proj/name", -1)
	if strings.Contains(withoutScratch, "--scratch-disk=") {
		t.Errorf("no scratch disk (idx=-1) must not emit --scratch-disk=; got %q", withoutScratch)
	}
}

// TestBootScratchDiskPresent_WorktreeShape is the mutation-proof for
// cmd_sandbox.go:bootScratchDiskPresent — the seam function that line 1732
// calls to compute HasScratchDisk. It must cover the LiveMount route:
// changing bootScratchDiskPresent to only check workspacePath != "" MUST
// make this test go RED.
//
// Worktree-sandbox shape (herdr --file): no --workspace, one /workspace/myrepo
// LiveMount. The service attaches a scratch disk (create.go step 4.9 fires on
// HasWorkspaceMount), so bootScratchDiskPresent must return true.
func TestBootScratchDiskPresent_WorktreeShape(t *testing.T) {
	t.Parallel()

	liveMounts := []domain.LiveMount{
		{HostPath: "/host/repo", GuestPath: "/workspace/myrepo"},
	}

	// Main assertion: no --workspace, but /workspace LiveMount → scratch present.
	if !bootScratchDiskPresent("", liveMounts) {
		t.Error("bootScratchDiskPresent must be true for a /workspace/myrepo LiveMount with no --workspace")
	}

	// Absence sanity: no workspace at all → no scratch.
	if bootScratchDiskPresent("", nil) {
		t.Error("bootScratchDiskPresent must be false with no workspace and no LiveMounts")
	}

	// Capture path: --workspace set → scratch even without LiveMounts.
	if !bootScratchDiskPresent("/host/repo", nil) {
		t.Error("bootScratchDiskPresent must be true when workspacePath is set")
	}

	// Non-workspace LiveMount must not trigger scratch.
	nonWS := []domain.LiveMount{{HostPath: "/host/cfg", GuestPath: "/run/nexus3/agentcfg-lower"}}
	if bootScratchDiskPresent("", nonWS) {
		t.Error("bootScratchDiskPresent must be false for a non-/workspace LiveMount")
	}
}

// TestBootScratchDiskPresent_CmdlineIntegration drives the full
// guestBootCmdline path for the worktree shape, confirming that --scratch-disk=
// is emitted when bootScratchDiskPresent would return true. Complements the
// absent-case tests by covering the presence side for the LiveMount route.
func TestBootScratchDiskPresent_CmdlineIntegration(t *testing.T) {
	t.Parallel()

	liveMounts := []domain.LiveMount{
		{HostPath: "/host/repo", GuestPath: "/workspace/myrepo"},
	}
	hasScratch := bootScratchDiskPresent("", liveMounts) // the production derivation
	if !hasScratch {
		t.Fatal("precondition: hasScratch must be true for this test to be meaningful")
	}

	// Virtiofs mounts don't occupy ExtraDisks; scratch is ExtraDisks[0] → /dev/vdb.
	scratchExtraDisks := []service.ExtraDisk{{Path: "/fake/scratch.img"}}
	scratchIdx := len(scratchExtraDisks) - 1 // = 0

	cmdline := guestBootCmdline(nil, " --auto-resize", "proj/worktree", scratchIdx)
	if !strings.Contains(cmdline, "--scratch-disk=/dev/vdb") {
		t.Errorf("worktree-shape cmdline must contain --scratch-disk=/dev/vdb; got %q", cmdline)
	}
}

// TestBuildSandboxDriverFactory_WorktreeShape_ScratchDiskInCmdline drives
// buildSandboxDriverFactory end-to-end for the herdr worktree-sandbox shape
// (no --workspace, workspace arrives as a virtiofs LiveMount, scratch is the
// sole ExtraDisk) and asserts --scratch-disk=/dev/vd<N> appears in the
// assembled kernel cmdline.
//
// This test kills Mutant B (cmd_seam.go:112-114 — force scratchIdx=-1):
// with that mutant active, scratchDiskCmdlineArg returns "" and the assertion
// fails. Both the previous helper-level tests (TestBootScratchDiskPresent_*
// and TestBootScratchDiskPresent_CmdlineIntegration) are immune to Mutant B
// because they call guestBootCmdline with a hardcoded scratchIdx — they never
// go through the ExtraDisks-length derivation inside the factory.
//
// Mutant A (cmd_sandbox.go:1742 — pass nil for liveMounts to bootScratchDiskPresent)
// is NOT killable from this test: Mutant A is inside the newDriver closure in
// the CLI command handler, which is one call-stack level above buildSandboxDriverFactory.
// Our test constructs the spec directly, so HasScratchDisk is computed in test
// code, not via line 1742. The smallest seam that would make Mutant A reachable
// is extracting the spec assembly from newDriver into a standalone function
// (e.g. buildLiveMountDriverSpec) that can be unit-tested independently.
func TestBuildSandboxDriverFactory_WorktreeShape_ScratchDiskInCmdline(t *testing.T) {
	t.Parallel()

	// Mirror the production derivation at cmd_sandbox.go:1742.
	// workspacePath is "" (worktree shape uses a LiveMount, not --workspace).
	liveMounts := []domain.LiveMount{
		{HostPath: "/host/repo", GuestPath: "/workspace/repo"},
	}
	hasScratch := bootScratchDiskPresent("", liveMounts)
	if !hasScratch {
		t.Fatal("precondition: bootScratchDiskPresent must be true for /workspace LiveMount with empty workspacePath")
	}

	spec := sandboxDriverSpec{
		// SBHandle non-empty triggers the guestBootCmdline path in cmd_seam.go.
		SBHandle: "proj/wt-regression",
		// HasScratchDisk is the value production sets at line 1742. Set it here
		// via the same helper so the production logic is mirrored, not bypassed.
		HasScratchDisk: hasScratch,
		// LiveMounts nil: avoids resolveVirtiofsdPath() in test environment where
		// virtiofsd is absent. HasScratchDisk is already computed above via
		// bootScratchDiskPresent, so the nil here does not affect the assertion.
		LiveMounts:  nil,
		GuestMounts: nil, // SBHandle suffices to select guestBootCmdline
	}

	var caps sandboxDriverCaptures
	factory := buildSandboxDriverFactory(spec, &caps)

	// One ExtraDisk: the scratch disk. Virtiofs mounts do not consume ExtraDisks
	// slots (they use tags), so the scratch disk is index 0 → /dev/vdb.
	// The factory populates caps.Cmdline before calling cloudhypervisor.New, so
	// the driver-creation error (binary absent in test env) is irrelevant here.
	_, _ = factory("/dev/null", []service.ExtraDisk{{Path: "/dev/null"}})

	if !strings.Contains(caps.Cmdline, "--scratch-disk=/dev/vdb") {
		t.Errorf("worktree-shape cmdline must contain --scratch-disk=/dev/vdb; got %q", caps.Cmdline)
	}
}

// TestBuildLiveMountDriverSpec_WorktreeShape kills Mutant A:
// changing bootLiveMounts to nil at the buildLiveMountDriverSpec call site in
// the newDriver closure (cmd_sandbox.go) causes bootScratchDiskPresent("", nil)
// to return false, and this assertion fails — the mutation is detected.
//
// Unlike TestBuildSandboxDriverFactory_WorktreeShape_ScratchDiskInCmdline, this
// test calls buildLiveMountDriverSpec directly, so HasScratchDisk is computed
// by the production code path at line 1742 (now inside buildLiveMountDriverSpec)
// rather than in test code. That is the code path Mutant A targets.
func TestBuildLiveMountDriverSpec_WorktreeShape(t *testing.T) {
	// Worktree shape: no --workspace flag (workspacePath == ""), but a
	// /workspace/<name> LiveMount is present (herdr --file path).
	liveMounts := []domain.LiveMount{
		{HostPath: "/host/myrepo", GuestPath: "/workspace/myrepo"},
	}

	f := sandboxCreateFlags{
		memoryMiB: 1024,
		vcpus:     2,
		// workspacePath is intentionally "" — worktree shape uses a LiveMount.
	}
	ar := vmcfg.Result{}

	spec := buildLiveMountDriverSpec(f, ar, "/boot/vmlinux", liveMounts, nil, nil, "proj", "wt")

	if !spec.HasScratchDisk {
		t.Error("buildLiveMountDriverSpec must set HasScratchDisk=true for a /workspace LiveMount with empty workspacePath")
	}
}

// TestBuildLiveMountDriverSpec_NoWorkspace_NoMount confirms that
// HasScratchDisk is false when neither --workspace nor a /workspace LiveMount
// is present — the negative case that would be vacuously true otherwise.
func TestBuildLiveMountDriverSpec_NoWorkspace_NoMount(t *testing.T) {
	f := sandboxCreateFlags{memoryMiB: 512, vcpus: 1}
	ar := vmcfg.Result{}

	spec := buildLiveMountDriverSpec(f, ar, "/boot/vmlinux", nil, nil, nil, "proj", "plain")

	if spec.HasScratchDisk {
		t.Error("buildLiveMountDriverSpec must set HasScratchDisk=false with no workspace and no LiveMounts")
	}
}

// TestBuildLiveMountDriverSpec_WorkspacePath confirms that HasScratchDisk is
// true when workspacePath is set (the --workspace ext4-capture route).
func TestBuildLiveMountDriverSpec_WorkspacePath(t *testing.T) {
	f := sandboxCreateFlags{
		memoryMiB:     512,
		vcpus:         1,
		workspacePath: "/host/ext4.img",
	}
	ar := vmcfg.Result{}

	spec := buildLiveMountDriverSpec(f, ar, "/boot/vmlinux", nil, nil, nil, "proj", "ws")

	if !spec.HasScratchDisk {
		t.Error("buildLiveMountDriverSpec must set HasScratchDisk=true when workspacePath is set")
	}
}

// TestBuildLiveMountDriverSpec_GuestMountsAssembly confirms that namedDiskMounts
// lead, followed by bootGuestMounts and the derived liveGuestMounts, matching
// the ExtraDisks layout contract (D-PD-53).
func TestBuildLiveMountDriverSpec_GuestMountsAssembly(t *testing.T) {
	named := []agent.GuestMount{{Device: "/dev/vdb"}}
	boot := []agent.GuestMount{{Device: "/dev/vdc"}}
	live := []domain.LiveMount{{HostPath: "/h", GuestPath: "/g"}}

	f := sandboxCreateFlags{memoryMiB: 512, vcpus: 1}
	ar := vmcfg.Result{}

	spec := buildLiveMountDriverSpec(f, ar, "/k", live, boot, named, "p", "n")

	if len(spec.GuestMounts) != 3 {
		t.Fatalf("expected 3 GuestMounts (named+boot+live), got %d", len(spec.GuestMounts))
	}
	if spec.GuestMounts[0].Device != "/dev/vdb" {
		t.Errorf("first GuestMount must be named disk; got %v", spec.GuestMounts[0])
	}
}

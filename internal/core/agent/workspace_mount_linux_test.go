//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// planMountOrder is a pure function: test it without root or real mounts.

func TestPlanMountOrderParentBeforeChild(t *testing.T) {
	// Input is intentionally in wrong order: shadow before parent.
	input := []GuestMount{
		{Device: "/dev/vdc", Target: "/workspace/repo/node_modules", FSType: "ext4"},
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4"},
	}
	got := planMountOrder(input)
	if len(got) != 2 {
		t.Fatalf("want 2 mounts, got %d", len(got))
	}
	if got[0].Target != "/workspace/repo" {
		t.Errorf("want first mount /workspace/repo, got %q", got[0].Target)
	}
	if got[1].Target != "/workspace/repo/node_modules" {
		t.Errorf("want second mount /workspace/repo/node_modules, got %q", got[1].Target)
	}
}

func TestPlanMountOrderMultipleShadows(t *testing.T) {
	// Multiple shadow disks at different depths; all depths scrambled.
	input := []GuestMount{
		{Device: "/dev/vde", Target: "/workspace/repo/target/debug", FSType: "ext4"},
		{Device: "/dev/vdc", Target: "/workspace/repo/node_modules", FSType: "ext4"},
		{Device: "/dev/vdd", Target: "/workspace/repo/target", FSType: "ext4"},
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4"},
	}
	got := planMountOrder(input)
	if len(got) != 4 {
		t.Fatalf("want 4 mounts, got %d", len(got))
	}

	pos := map[string]int{}
	for i, m := range got {
		pos[m.Target] = i
	}

	// Parent must precede all children.
	cases := [][2]string{
		{"/workspace/repo", "/workspace/repo/node_modules"},
		{"/workspace/repo", "/workspace/repo/target"},
		{"/workspace/repo/target", "/workspace/repo/target/debug"},
	}
	for _, c := range cases {
		if pos[c[0]] >= pos[c[1]] {
			t.Errorf("%s must come before %s (positions %d, %d)",
				c[0], c[1], pos[c[0]], pos[c[1]])
		}
	}
}

func TestPlanMountOrderAlreadySorted(t *testing.T) {
	// When already in dependency order, output must preserve that order.
	input := []GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4"},
		{Device: "/dev/vdc", Target: "/workspace/repo/node_modules", FSType: "ext4"},
	}
	got := planMountOrder(input)
	if got[0].Target != "/workspace/repo" || got[1].Target != "/workspace/repo/node_modules" {
		t.Errorf("unexpected order: %q then %q", got[0].Target, got[1].Target)
	}
}

func TestPlanMountOrderDeterministicAtSameDepth(t *testing.T) {
	// Two sibling mounts at the same depth → sorted lexicographically by Target.
	input := []GuestMount{
		{Device: "/dev/vdc", Target: "/workspace/repo/node_modules", FSType: "ext4"},
		{Device: "/dev/vdb", Target: "/workspace/repo/.next", FSType: "ext4"},
	}
	got := planMountOrder(input)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Target >= got[1].Target {
		t.Errorf("expected lexicographic order, got %q before %q", got[0].Target, got[1].Target)
	}
}

func TestPlanMountOrderDoesNotMutateInput(t *testing.T) {
	// planMountOrder must return a fresh slice; the original must be unchanged.
	input := []GuestMount{
		{Device: "/dev/vdc", Target: "/workspace/repo/node_modules", FSType: "ext4"},
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4"},
	}
	origFirst := input[0].Target
	planMountOrder(input)
	if input[0].Target != origFirst {
		t.Errorf("planMountOrder must not mutate input slice; was %q, now %q", origFirst, input[0].Target)
	}
}

func TestPlanMountOrderEmptyAndSingle(t *testing.T) {
	// Empty input → empty output; no panic.
	got := planMountOrder(nil)
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}

	// Single mount → returned unchanged.
	single := []GuestMount{{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4"}}
	got = planMountOrder(single)
	if len(got) != 1 || got[0].Target != "/workspace/repo" {
		t.Errorf("unexpected single-element result: %v", got)
	}
}

// TestPlanMountOrder_MixedExt4AndVirtioFS verifies that virtiofs mounts
// participate in the existing parent-before-child ordering (invariant D-DC-10)
// and are NOT special-cased around it. The scenario: an ext4 shadow disk is
// nested under a virtiofs workspace mount.
//
//	/workspace/repo          → virtiofs (workspace tag)
//	/workspace/repo/node_modules → ext4 (shadow disk)
//
// Input is intentionally reversed; planMountOrder must restore the correct
// order so the ext4 shadow is never attempted before the virtiofs parent.
func TestPlanMountOrder_MixedExt4AndVirtioFS(t *testing.T) {
	input := []GuestMount{
		// ext4 shadow disk nested under virtiofs — intentionally listed first (wrong order).
		{Device: "/dev/vdb", Target: "/workspace/repo/node_modules", FSType: "ext4"},
		// virtiofs workspace mount — parent, must be mounted first.
		{Device: "workspace-tag", Target: "/workspace/repo", FSType: "virtiofs", IsWorkspace: true},
	}
	got := planMountOrder(input)
	if len(got) != 2 {
		t.Fatalf("want 2 mounts, got %d", len(got))
	}
	// virtiofs parent must come first.
	if got[0].Target != "/workspace/repo" {
		t.Errorf("want first mount /workspace/repo (virtiofs), got %q (fstype=%s)", got[0].Target, got[0].FSType)
	}
	if got[0].FSType != "virtiofs" {
		t.Errorf("first mount must be virtiofs, got fstype=%q", got[0].FSType)
	}
	// ext4 shadow must come second.
	if got[1].Target != "/workspace/repo/node_modules" {
		t.Errorf("want second mount /workspace/repo/node_modules (ext4), got %q (fstype=%s)", got[1].Target, got[1].FSType)
	}
	if got[1].FSType != "ext4" {
		t.Errorf("second mount must be ext4, got fstype=%q", got[1].FSType)
	}
}

// TestMountFlags_VirtioFSReadOnly asserts that a virtiofs GuestMount with
// ReadOnly=true produces syscall.MS_RDONLY via mountFlags(). This pins the
// read-only contract for virtiofs: the existing mountFlags() implementation
// is FSType-agnostic, so virtiofs inherits MS_RDONLY without any special case.
func TestMountFlags_VirtioFSReadOnly(t *testing.T) {
	ro := GuestMount{Device: "shared-tag", Target: "/workspace/shared", FSType: "virtiofs", ReadOnly: true}
	rw := GuestMount{Device: "workspace-tag", Target: "/workspace/repo", FSType: "virtiofs", ReadOnly: false}

	if ro.mountFlags() != syscall.MS_RDONLY {
		t.Errorf("virtiofs ReadOnly=true: want MS_RDONLY (%d), got %d", syscall.MS_RDONLY, ro.mountFlags())
	}
	if rw.mountFlags() != 0 {
		t.Errorf("virtiofs ReadOnly=false: want 0 flags, got %d", rw.mountFlags())
	}
}

func TestMountFileVirtiofs_BindCallArgs(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(t.TempDir(), ".tmux.conf")

	origBase := fileMountScratchBase
	origTag := guestVirtiofsTagMountFn
	origBind := guestBindMountFn
	t.Cleanup(func() {
		fileMountScratchBase = origBase
		guestVirtiofsTagMountFn = origTag
		guestBindMountFn = origBind
	})

	fileMountScratchBase = base

	var tagDevice, tagTarget string
	var tagFlags uintptr
	guestVirtiofsTagMountFn = func(device, tgt string, flags uintptr) error {
		tagDevice = device
		tagTarget = tgt
		tagFlags = flags
		return os.WriteFile(filepath.Join(tgt, ".tmux.conf"), []byte("# test"), 0o600)
	}

	var bindSrc, bindDst string
	var bindFlags uintptr
	guestBindMountFn = func(src, dst string, flags uintptr) error {
		bindSrc = src
		bindDst = dst
		bindFlags = flags
		return nil
	}

	m := GuestMount{
		Device:   "nxfs4",
		Target:   target,
		FSType:   "virtiofs",
		ReadOnly: true,
		IsFile:   true,
		FileName: ".tmux.conf",
	}
	if err := mountFileVirtiofs(m); err != nil {
		t.Fatalf("mountFileVirtiofs: %v", err)
	}

	if tagDevice != "nxfs4" {
		t.Errorf("tag mount device = %q, want nxfs4", tagDevice)
	}
	if tagTarget != filepath.Join(base, "nxfs4") {
		t.Errorf("tag mount target = %q, want %q", tagTarget, filepath.Join(base, "nxfs4"))
	}
	if tagFlags != syscall.MS_RDONLY {
		t.Errorf("tag mount flags = %d, want MS_RDONLY (%d)", tagFlags, syscall.MS_RDONLY)
	}
	wantSrc := filepath.Join(base, "nxfs4", ".tmux.conf")
	if bindSrc != wantSrc {
		t.Errorf("bind src = %q, want %q", bindSrc, wantSrc)
	}
	if bindDst != target {
		t.Errorf("bind dst = %q, want %q", bindDst, target)
	}
	if bindFlags != syscall.MS_RDONLY {
		t.Errorf("bind flags = %d, want MS_RDONLY (%d)", bindFlags, syscall.MS_RDONLY)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("target file not created: %v", err)
	}
}

func TestMountFileVirtiofs_RW(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(t.TempDir(), ".gitconfig")

	origBase := fileMountScratchBase
	origTag := guestVirtiofsTagMountFn
	origBind := guestBindMountFn
	t.Cleanup(func() {
		fileMountScratchBase = origBase
		guestVirtiofsTagMountFn = origTag
		guestBindMountFn = origBind
	})
	fileMountScratchBase = base
	guestVirtiofsTagMountFn = func(device, tgt string, flags uintptr) error {
		return os.WriteFile(filepath.Join(tgt, ".gitconfig"), []byte("[user]"), 0o600)
	}

	var bindFlags uintptr
	guestBindMountFn = func(_, _ string, flags uintptr) error {
		bindFlags = flags
		return nil
	}

	m := GuestMount{Device: "nxfs1", Target: target, FSType: "virtiofs", ReadOnly: false, IsFile: true, FileName: ".gitconfig"}
	if err := mountFileVirtiofs(m); err != nil {
		t.Fatalf("mountFileVirtiofs rw: %v", err)
	}
	if bindFlags != 0 {
		t.Errorf("rw bind flags = %d, want 0", bindFlags)
	}
}

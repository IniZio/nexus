//go:build linux

package agent

import (
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

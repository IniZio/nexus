//go:build linux

package agent

import (
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

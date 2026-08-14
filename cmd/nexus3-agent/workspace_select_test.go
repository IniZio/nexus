package main

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
)

// defaultTopology returns a realistic mount slice matching the default
// nexus3 sandbox configuration: 4 shadow disks (node_modules, .next,
// target, dist) followed by the workspace disk — exactly the order
// produced by cmd_sandbox.go:
//
//	allMounts := append(shadowGuestMounts(shadowSpecs, 0),
//	    WorkspaceGuestMount(guestPath, len(shadowSpecs)))
//
// Shadow mounts have IsWorkspace=false. The workspace mount has IsWorkspace=true,
// matching what WorkspaceGuestMount returns after the fix.
func defaultTopology(guestPath string) []agent.GuestMount {
	return []agent.GuestMount{
		// Shadow disks: vdb–vde (DefaultShadowDirs: node_modules, .next, target, dist).
		// IsWorkspace is intentionally unset (false) for all shadows.
		{Device: "/dev/vdb", Target: guestPath + "/node_modules", FSType: "ext4"},
		{Device: "/dev/vdc", Target: guestPath + "/.next", FSType: "ext4"},
		{Device: "/dev/vdd", Target: guestPath + "/target", FSType: "ext4"},
		{Device: "/dev/vde", Target: guestPath + "/dist", FSType: "ext4"},
		// Workspace disk: vdf. IsWorkspace=true — the marker set by WorkspaceGuestMount.
		{Device: "/dev/vdf", Target: guestPath, FSType: "ext4", IsWorkspace: true},
	}
}

// TestSelectWorkspaceMount_DefaultTopology is the primary regression test.
//
// It constructs the realistic default topology — 4 shadow dirs + workspace —
// and asserts that selectWorkspaceMount returns the workspace path, not
// node_modules. This test must fail against the old positional/ReadOnly logic
// (which picks wsMounts[0] = node_modules).
func TestSelectWorkspaceMount_DefaultTopology(t *testing.T) {
	const guestPath = "/workspace/repo"
	mounts := defaultTopology(guestPath)

	wsMount, ok, err := selectWorkspaceMount(mounts)
	if err != nil {
		t.Fatalf("selectWorkspaceMount: unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("selectWorkspaceMount: returned ok=false; expected the workspace mount to be found")
	}
	if wsMount.Target != guestPath {
		t.Errorf("selectWorkspaceMount returned Target=%q; want %q (a shadow disk was selected instead of the workspace)",
			wsMount.Target, guestPath)
	}
	if !wsMount.IsWorkspace {
		t.Errorf("selected mount has IsWorkspace=false; want true")
	}
}

// TestSelectWorkspaceMount_WorkspaceOnly verifies the single-mount case with
// no shadows works correctly.
func TestSelectWorkspaceMount_WorkspaceOnly(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4", IsWorkspace: true},
	}
	wsMount, ok, err := selectWorkspaceMount(mounts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok=false; want true")
	}
	if wsMount.Target != "/workspace/repo" {
		t.Errorf("Target=%q; want /workspace/repo", wsMount.Target)
	}
}

// TestSelectWorkspaceMount_NoWorkspaceMount verifies that a slice containing
// only shadow mounts (no IsWorkspace=true) returns ok=false without panicking.
func TestSelectWorkspaceMount_NoWorkspaceMount(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo/node_modules", FSType: "ext4"},
		{Device: "/dev/vdc", Target: "/workspace/repo/.next", FSType: "ext4"},
	}
	_, ok, err := selectWorkspaceMount(mounts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("ok=true; want false (no IsWorkspace=true mount present)")
	}
}

// TestSelectWorkspaceMount_EmptySlice verifies that an empty mount list
// returns ok=false without panicking.
func TestSelectWorkspaceMount_EmptySlice(t *testing.T) {
	_, ok, err := selectWorkspaceMount(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("ok=true; want false for empty slice")
	}
}

// TestSelectWorkspaceMount_MultipleWorkspaceMounts verifies that two mounts
// with IsWorkspace=true produce an error rather than silently picking the first.
func TestSelectWorkspaceMount_MultipleWorkspaceMounts(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/a", FSType: "ext4", IsWorkspace: true},
		{Device: "/dev/vdc", Target: "/workspace/b", FSType: "ext4", IsWorkspace: true},
	}
	_, ok, err := selectWorkspaceMount(mounts)
	if err == nil {
		t.Error("expected an error for two IsWorkspace=true mounts; got nil")
	}
	if ok {
		t.Error("ok=true; want false when multiple workspace mounts are present")
	}
}

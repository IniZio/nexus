package cli

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
)

// TestWorkspaceGuestMount_DeviceOrder verifies the device-letter derivation
// for various shadow-disk counts without booting a VM.
//
// Contract (D-DC-10):
//
//	ExtraDisks[i]   → /dev/vd{b+i}   (shadow disk i, 0-indexed)
//	ExtraDisks[N]   → /dev/vd{b+N}   (workspace disk; appended last by service)
func TestWorkspaceGuestMount_DeviceOrder(t *testing.T) {
	cases := []struct {
		numShadow  int
		wantDevice string
	}{
		{0, "/dev/vdb"}, // no shadows → workspace at vdb
		{1, "/dev/vdc"}, // 1 shadow (vdb) → workspace at vdc
		{2, "/dev/vdd"},
		{3, "/dev/vde"},
		{4, "/dev/vdf"}, // DefaultShadowDirs has 4 entries → workspace at vdf
		{5, "/dev/vdg"},
	}
	for _, tc := range cases {
		got := WorkspaceGuestMount("/workspace/repo", tc.numShadow)
		if got.Device != tc.wantDevice {
			t.Errorf("numShadow=%d: got Device=%q, want %q", tc.numShadow, got.Device, tc.wantDevice)
		}
		if got.Target != "/workspace/repo" {
			t.Errorf("numShadow=%d: got Target=%q, want /workspace/repo", tc.numShadow, got.Target)
		}
		if got.FSType != "ext4" {
			t.Errorf("numShadow=%d: got FSType=%q, want ext4", tc.numShadow, got.FSType)
		}
		if got.ReadOnly {
			t.Errorf("numShadow=%d: workspace mount should be read-write", tc.numShadow)
		}
		if !got.IsWorkspace {
			t.Errorf("numShadow=%d: WorkspaceGuestMount should set IsWorkspace=true", tc.numShadow)
		}
	}
}

// TestShadowGuestMounts_DeviceOrder verifies that shadow disks get the correct
// /dev/vd{b+i} letters when there are no preceding extra disks (offset=0).
func TestShadowGuestMounts_DeviceOrder(t *testing.T) {
	specs := []ShadowDisk{
		{RelDir: "node_modules", GuestTarget: "/workspace/repo/node_modules"},
		{RelDir: ".next", GuestTarget: "/workspace/repo/.next"},
		{RelDir: "target", GuestTarget: "/workspace/repo/target"},
		{RelDir: "dist", GuestTarget: "/workspace/repo/dist"},
	}
	wantDevices := []string{"/dev/vdb", "/dev/vdc", "/dev/vdd", "/dev/vde"}

	got := shadowGuestMounts(specs, 0)
	if len(got) != len(wantDevices) {
		t.Fatalf("shadowGuestMounts returned %d mounts, want %d", len(got), len(wantDevices))
	}
	for i, m := range got {
		if m.Device != wantDevices[i] {
			t.Errorf("shadow[%d]: got Device=%q, want %q", i, m.Device, wantDevices[i])
		}
		if m.Target != specs[i].GuestTarget {
			t.Errorf("shadow[%d]: got Target=%q, want %q", i, m.Target, specs[i].GuestTarget)
		}
		if m.IsWorkspace {
			t.Errorf("shadow[%d]: IsWorkspace should be false for shadow mounts", i)
		}
	}
}

// TestDefaultShadowDirsCount ensures DefaultShadowDirs has exactly 4 entries,
// pinning the workspace device letter (/dev/vdf) for the default configuration.
func TestDefaultShadowDirsCount(t *testing.T) {
	const want = 4
	if got := len(DefaultShadowDirs); got != want {
		t.Errorf("len(DefaultShadowDirs)=%d, want %d; workspace device letter has shifted — update this test and consumers", got, want)
	}
}

// TestWorkspaceMountCmdline_Empty verifies that an empty mount list returns the base cmdline.
func TestWorkspaceMountCmdline_Empty(t *testing.T) {
	got := workspaceMountCmdline([]agent.GuestMount{})
	// An empty slice should still produce the base cmdline + " --" because the
	// caller guards with len>0, but workspaceMountCmdline itself must not panic.
	// We verify it starts with the base.
	if !strings.HasPrefix(got, diskBootCmdlineBase) {
		t.Errorf("got %q, want prefix %q", got, diskBootCmdlineBase)
	}
}

// TestWorkspaceMountCmdline_SingleMount verifies encoding for a single workspace mount.
// The format is now 5 fields: device:target:fstype:readonly:workspace.
func TestWorkspaceMountCmdline_SingleMount(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4", ReadOnly: false, IsWorkspace: false},
	}
	got := workspaceMountCmdline(mounts)
	want := diskBootCmdlineBase + " -- --workspace-mount=/dev/vdb:/workspace/repo:ext4:false:false"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestWorkspaceMountCmdline_DefaultLayout verifies the cmdline for the default
// 4-shadow + workspace configuration: shadows on vdb–vde, workspace on vdf.
func TestWorkspaceMountCmdline_DefaultLayout(t *testing.T) {
	const guestPath = "/workspace/repo"
	// Simulate what cmd_sandbox.go computes: 4 shadow disks + workspace.
	shadowSpecs := []ShadowDisk{
		{RelDir: "node_modules", GuestTarget: guestPath + "/node_modules"},
		{RelDir: ".next", GuestTarget: guestPath + "/.next"},
		{RelDir: "target", GuestTarget: guestPath + "/target"},
		{RelDir: "dist", GuestTarget: guestPath + "/dist"},
	}
	mounts := append(shadowGuestMounts(shadowSpecs, 0),
		WorkspaceGuestMount(guestPath, len(shadowSpecs)))

	got := workspaceMountCmdline(mounts)

	// Must start with base cmdline and " --" separator.
	if !strings.HasPrefix(got, diskBootCmdlineBase+" --") {
		t.Errorf("cmdline missing base+separator prefix: %q", got)
	}

	// Must contain exactly 5 --workspace-mount tokens.
	count := strings.Count(got, "--workspace-mount=")
	if count != 5 {
		t.Errorf("want 5 --workspace-mount tokens, got %d in: %q", count, got)
	}

	// Workspace disk must be on /dev/vdf (4 shadows + 1 workspace).
	// ReadOnly=false → ":false"; IsWorkspace=true → ":true" (5th field).
	wsMountToken := "--workspace-mount=/dev/vdf:" + guestPath + ":ext4:false:true"
	if !strings.Contains(got, wsMountToken) {
		t.Errorf("workspace mount token %q not found in cmdline: %q", wsMountToken, got)
	}

	// Shadow for node_modules must be on /dev/vdb (index 0).
	// ReadOnly=false → ":false"; IsWorkspace=false → ":false" (5th field).
	shadowToken := "--workspace-mount=/dev/vdb:" + guestPath + "/node_modules:ext4:false:false"
	if !strings.Contains(got, shadowToken) {
		t.Errorf("shadow mount token %q not found in cmdline: %q", shadowToken, got)
	}
}

// TestWorkspaceMountCmdline_ReadOnly verifies that ReadOnly=true maps to ":true" suffix.
func TestWorkspaceMountCmdline_ReadOnly(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4", ReadOnly: true},
	}
	got := workspaceMountCmdline(mounts)
	if !strings.Contains(got, ":true") {
		t.Errorf("expected :true suffix for ReadOnly=true mount, got: %q", got)
	}
}

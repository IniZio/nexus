package main

// Unit tests for the platform-agnostic resize-disk helpers: diskIndexFromDevice,
// resizableDisksFromWorkspaceMounts, and resizableDisksFromCacheDisks.
// No build tag — these helpers are compiled on all platforms.

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
)

// ── diskIndexFromDevice ───────────────────────────────────────────────────────

func TestDiskIndexFromDevice(t *testing.T) {
	cases := []struct {
		device  string
		wantIdx int
		wantOK  bool
	}{
		{"/dev/vdb", 0, true},
		{"/dev/vdc", 1, true},
		{"/dev/vdd", 2, true},
		{"/dev/vdz", 24, true},
		// Invalid: virtiofs tag, not a /dev/vd* path.
		{"workspace-tag", 0, false},
		// Invalid: letter before 'b'.
		{"/dev/vda", 0, false},
		// Invalid: too short / too long.
		{"/dev/vd", 0, false},
		{"/dev/vdbb", 0, false},
		// Invalid: empty.
		{"", 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.device, func(t *testing.T) {
			idx, ok := diskIndexFromDevice(tc.device)
			if ok != tc.wantOK {
				t.Errorf("diskIndexFromDevice(%q) ok = %v, want %v", tc.device, ok, tc.wantOK)
			}
			if ok && idx != tc.wantIdx {
				t.Errorf("diskIndexFromDevice(%q) index = %d, want %d", tc.device, idx, tc.wantIdx)
			}
		})
	}
}

// ── resizableDisksFromWorkspaceMounts ─────────────────────────────────────────

func TestResizableDisksFromWorkspaceMounts_ExtractWorkspaceOnly(t *testing.T) {
	mounts := []agent.GuestMount{
		// shadow disk — must be skipped (not workspace, not resizable)
		{Device: "/dev/vdb", Target: "/workspace/shadow", FSType: "ext4", ReadOnly: false, IsWorkspace: false},
		// workspace disk at vdc (index 1)
		{Device: "/dev/vdc", Target: "/workspace/repo", FSType: "ext4", ReadOnly: false, IsWorkspace: true},
	}
	got := resizableDisksFromWorkspaceMounts(mounts)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Index != 1 {
		t.Errorf("Index = %d, want 1 (/dev/vdc)", got[0].Index)
	}
	if got[0].MountPath != "/workspace/repo" {
		t.Errorf("MountPath = %q, want /workspace/repo", got[0].MountPath)
	}
}

func TestResizableDisksFromWorkspaceMounts_VirtiofsSkipped(t *testing.T) {
	// virtiofs workspace mount: device is a tag, not /dev/vd* — must be skipped.
	mounts := []agent.GuestMount{
		{Device: "workspace-tag", Target: "/workspace/repo", FSType: "virtiofs", ReadOnly: false, IsWorkspace: true},
	}
	got := resizableDisksFromWorkspaceMounts(mounts)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (virtiofs workspace has no derivable index)", len(got))
	}
}

func TestResizableDisksFromWorkspaceMounts_NoWorkspaceMount(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/workspace/repo", FSType: "ext4", IsWorkspace: false},
	}
	got := resizableDisksFromWorkspaceMounts(mounts)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (not workspace, not resizable)", len(got))
	}
}

// TestResizableDisksFromWorkspaceMounts_ResizableNamedDisk verifies that a
// Resizable=true named-volume mount (e.g. /var/lib/docker) is included in the
// telemetry list at its correct device index, independent of IsWorkspace.
//
// MUTATION PROOF: removing `|| m.Resizable` from the filter in
// resizableDisksFromWorkspaceMounts causes this test to return 1 entry
// (workspace only) instead of 2, failing the len assertion.
func TestResizableDisksFromWorkspaceMounts_ResizableNamedDisk(t *testing.T) {
	// Sandbox layout: docker vol (vdb=0, Resizable), workspace (vdc=1, IsWorkspace).
	mounts := []agent.GuestMount{
		{Device: "/dev/vdb", Target: "/var/lib/docker", FSType: "ext4", Resizable: true},
		{Device: "/dev/vdc", Target: "/workspace/repo", FSType: "ext4", IsWorkspace: true},
	}
	got := resizableDisksFromWorkspaceMounts(mounts)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (docker + workspace)", len(got))
	}
	// Verify each entry by index (order may vary by implementation).
	byIdx := map[int]string{}
	for _, d := range got {
		byIdx[d.Index] = d.MountPath
	}
	if byIdx[0] != "/var/lib/docker" {
		t.Errorf("index 0 MountPath = %q, want /var/lib/docker", byIdx[0])
	}
	if byIdx[1] != "/workspace/repo" {
		t.Errorf("index 1 MountPath = %q, want /workspace/repo", byIdx[1])
	}
}

// TestResizableDisksFromWorkspaceMounts_ResizableVirtiofsSkipped verifies that
// a Resizable=true virtiofs mount is silently skipped (index cannot be derived).
func TestResizableDisksFromWorkspaceMounts_ResizableVirtiofsSkipped(t *testing.T) {
	mounts := []agent.GuestMount{
		{Device: "some-tag", Target: "/mnt/dir", FSType: "virtiofs", Resizable: true},
	}
	got := resizableDisksFromWorkspaceMounts(mounts)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (virtiofs Resizable mount has no derivable index)", len(got))
	}
}

// ── resizableDisksFromCacheDisks ──────────────────────────────────────────────

func TestResizableDisksFromCacheDisks(t *testing.T) {
	// Builder VM layout: vdb=context(0), vdc=artifact(1), vdd=buildkit(2), vde=npm(3).
	cacheDisks := []agent.CacheDiskMount{
		{Device: "/dev/vdd", MountPath: "/var/lib/buildkit"},
		{Device: "/dev/vde", MountPath: "/root/.npm"},
	}
	got := resizableDisksFromCacheDisks(cacheDisks)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Index != 2 {
		t.Errorf("got[0].Index = %d, want 2 (/dev/vdd)", got[0].Index)
	}
	if got[0].MountPath != "/var/lib/buildkit" {
		t.Errorf("got[0].MountPath = %q, want /var/lib/buildkit", got[0].MountPath)
	}
	if got[1].Index != 3 {
		t.Errorf("got[1].Index = %d, want 3 (/dev/vde)", got[1].Index)
	}
	if got[1].MountPath != "/root/.npm" {
		t.Errorf("got[1].MountPath = %q, want /root/.npm", got[1].MountPath)
	}
}

// ── selectResizableDisks ──────────────────────────────────────────────────────

// TestSelectResizableDisks_BuilderMode verifies builder mode returns cache disks
// (incl. /dev/vdd → index 2) and never the workspace disk list.
func TestSelectResizableDisks_BuilderMode(t *testing.T) {
	cacheDisks := []agent.CacheDiskMount{
		{Device: "/dev/vdd", MountPath: "/var/lib/buildkit"},
	}
	// wsDisks would be empty in a real builder VM; pass a non-empty slice to
	// prove it is ignored when isBuilderRole=true.
	wsDisks := []resizableDisk{{Index: 0, MountPath: "/workspace"}}

	got := selectResizableDisks(true, cacheDisks, wsDisks)
	if len(got) != 1 {
		t.Fatalf("builder mode: len = %d, want 1", len(got))
	}
	if got[0].Index != 2 {
		t.Errorf("builder mode: Index = %d, want 2 (/dev/vdd)", got[0].Index)
	}
	if got[0].MountPath != "/var/lib/buildkit" {
		t.Errorf("builder mode: MountPath = %q, want /var/lib/buildkit", got[0].MountPath)
	}
}

// TestSelectResizableDisks_NormalMode verifies normal mode returns wsDisks
// unchanged and never touches cacheDisks.
func TestSelectResizableDisks_NormalMode(t *testing.T) {
	cacheDisks := []agent.CacheDiskMount{
		{Device: "/dev/vdd", MountPath: "/var/lib/buildkit"},
	}
	wsDisks := []resizableDisk{{Index: 1, MountPath: "/workspace/repo"}}

	got := selectResizableDisks(false, cacheDisks, wsDisks)
	if len(got) != 1 {
		t.Fatalf("normal mode: len = %d, want 1", len(got))
	}
	if got[0].Index != 1 {
		t.Errorf("normal mode: Index = %d, want 1", got[0].Index)
	}
	if got[0].MountPath != "/workspace/repo" {
		t.Errorf("normal mode: MountPath = %q, want /workspace/repo", got[0].MountPath)
	}
}

// TestSelectResizableDisks_BuilderNoCacheDisks verifies that a builder VM with
// no recognizable cache-disk devices returns an empty list (not a nil panic).
func TestSelectResizableDisks_BuilderNoCacheDisks(t *testing.T) {
	got := selectResizableDisks(true, nil, nil)
	if len(got) != 0 {
		t.Errorf("builder mode, no cache disks: len = %d, want 0", len(got))
	}
}

func TestResizableDisksFromCacheDisks_UnrecognisedDeviceSkipped(t *testing.T) {
	cacheDisks := []agent.CacheDiskMount{
		{Device: "/dev/sda", MountPath: "/mnt/sda"},   // not /dev/vd* → skipped
		{Device: "/dev/vdd", MountPath: "/mnt/cache"},  // ok
	}
	got := resizableDisksFromCacheDisks(cacheDisks)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (sda must be skipped)", len(got))
	}
	if got[0].Index != 2 {
		t.Errorf("got[0].Index = %d, want 2", got[0].Index)
	}
}

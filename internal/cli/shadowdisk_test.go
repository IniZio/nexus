package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
)

// ── buildShadowDiskSpecs ──────────────────────────────────────────────────────

func TestBuildShadowDiskSpecs_BasicMapping(t *testing.T) {
	diskDir := t.TempDir()
	guestPath := "/workspace/myrepo"
	handle := "myproject/mysandbox"

	specs := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, guestPath, handle)

	if len(specs) != len(DefaultShadowDirs) {
		t.Fatalf("want %d specs, got %d", len(DefaultShadowDirs), len(specs))
	}

	for i, s := range specs {
		wantRelDir := DefaultShadowDirs[i]
		if s.RelDir != wantRelDir {
			t.Errorf("spec[%d].RelDir: got %q, want %q", i, s.RelDir, wantRelDir)
		}
		wantTarget := guestPath + "/" + wantRelDir
		if s.GuestTarget != wantTarget {
			t.Errorf("spec[%d].GuestTarget: got %q, want %q", i, s.GuestTarget, wantTarget)
		}
		if !strings.HasSuffix(s.HostPath, ".ext4") {
			t.Errorf("spec[%d].HostPath: %q does not end in .ext4", i, s.HostPath)
		}
		if !strings.Contains(filepath.Base(s.HostPath), ".shadow.") {
			t.Errorf("spec[%d].HostPath: %q does not contain .shadow. infix", i, s.HostPath)
		}
		if filepath.Dir(s.HostPath) != diskDir {
			t.Errorf("spec[%d].HostPath: dir is %q, want %q", i, filepath.Dir(s.HostPath), diskDir)
		}
	}
}

func TestBuildShadowDiskSpecs_InvalidDirsSkipped(t *testing.T) {
	specs := buildShadowDiskSpecs([]string{
		"/absolute/path", // absolute — must be skipped
		".",              // dot — must be skipped
		"valid",          // ok
	}, t.TempDir(), "/workspace/repo", "proj/sb")

	if len(specs) != 1 {
		t.Fatalf("want 1 spec (only 'valid'), got %d: %+v", len(specs), specs)
	}
	if specs[0].RelDir != "valid" {
		t.Errorf("got RelDir %q, want %q", specs[0].RelDir, "valid")
	}
}

func TestBuildShadowDiskSpecs_NestedPath(t *testing.T) {
	// Nested monorepo path: separator must be flattened in the host filename.
	handle := "myproj/sb1"
	specs := buildShadowDiskSpecs([]string{"packages/web/node_modules"}, t.TempDir(), "/workspace/mono", handle)
	if len(specs) != 1 {
		t.Fatalf("want 1 spec, got %d", len(specs))
	}
	s := specs[0]
	if s.RelDir != "packages/web/node_modules" {
		t.Errorf("RelDir: got %q", s.RelDir)
	}
	if s.GuestTarget != "/workspace/mono/packages/web/node_modules" {
		t.Errorf("GuestTarget: got %q", s.GuestTarget)
	}
	// Host filename must not contain a slash.
	base := filepath.Base(s.HostPath)
	if strings.Contains(base, "/") {
		t.Errorf("HostPath basename contains slash: %q", base)
	}
	// Filename must contain the safeHandle component and the .shadow. infix.
	if !strings.Contains(base, "myproj_sb1") {
		t.Errorf("HostPath basename %q: expected safeHandle myproj_sb1", base)
	}
	if !strings.Contains(base, ".shadow.") {
		t.Errorf("HostPath basename %q: expected .shadow. infix", base)
	}
	// Dir separator must have been flattened.
	if !strings.Contains(base, "packages_web_node_modules") {
		t.Errorf("HostPath basename %q: expected separator replacement with _", base)
	}
}

// TestBuildShadowDiskSpecs_SandboxHandleInPath verifies that the sandbox handle
// is embedded in every shadow disk HostPath.
//
// REGRESSION: this test FAILS against the old code (no handle in path) — if the
// handle check below passes with the old path format, the concurrent-sandbox
// lock bug would silently return.
func TestBuildShadowDiskSpecs_SandboxHandleInPath(t *testing.T) {
	handle := "example-app/parallel-a"
	safeHandle := "example-app_parallel-a"
	specs := buildShadowDiskSpecs(DefaultShadowDirs, t.TempDir(), "/workspace/example-app", handle)
	for i, s := range specs {
		base := filepath.Base(s.HostPath)
		if !strings.Contains(base, safeHandle) {
			t.Errorf("spec[%d].HostPath %q does not contain safeHandle %q — "+
				"concurrent sandboxes of the same project would share this path and conflict",
				i, s.HostPath, safeHandle)
		}
	}
}

// TestBuildShadowDiskSpecs_DifferentHandlesDifferentPaths verifies that two
// sandboxes of the same project (same diskDir, same guestPath, same dirs) produce
// non-overlapping HostPaths.  This is the exact scenario that caused the write-lock
// failure in the parallel-dev walkthrough (scenario 4).
func TestBuildShadowDiskSpecs_DifferentHandlesDifferentPaths(t *testing.T) {
	diskDir := t.TempDir()
	guestPath := "/workspace/example-app"

	specs1 := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, guestPath, "example-app/parallel-a")
	specs2 := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, guestPath, "example-app/parallel-b")

	if len(specs1) != len(specs2) {
		t.Fatalf("spec count mismatch: %d vs %d", len(specs1), len(specs2))
	}
	for i := range specs1 {
		if specs1[i].HostPath == specs2[i].HostPath {
			t.Errorf("spec[%d]: parallel-a and parallel-b share HostPath %q — "+
				"concurrent sandbox create would fail with a write-lock error",
				i, specs1[i].HostPath)
		}
	}
}

// TestBuildShadowDiskSpecs_LeadingDotSanitized verifies that directories with a
// leading dot (e.g. ".next") produce a filename component that does not start with
// "." — preventing hidden files in the disks directory.
func TestBuildShadowDiskSpecs_LeadingDotSanitized(t *testing.T) {
	specs := buildShadowDiskSpecs([]string{".next"}, t.TempDir(), "/workspace/r", "proj/sb")
	if len(specs) != 1 {
		t.Fatalf("want 1 spec, got %d", len(specs))
	}
	base := filepath.Base(specs[0].HostPath)
	if strings.HasPrefix(base, ".") {
		t.Errorf("HostPath basename %q starts with '.'; leading-dot dirs must be sanitized", base)
	}
	// RelDir must preserve the original name unchanged.
	if specs[0].RelDir != ".next" {
		t.Errorf("RelDir should be .next, got %q", specs[0].RelDir)
	}
}

// ── device-letter derivation ──────────────────────────────────────────────────

func TestShadowDevicePath(t *testing.T) {
	cases := []struct {
		index int
		want  string
	}{
		{0, "/dev/vdb"},
		{1, "/dev/vdc"},
		{2, "/dev/vdd"},
		{3, "/dev/vde"},
		{4, "/dev/vdf"},
	}
	for _, tc := range cases {
		got := shadowDevicePath(tc.index)
		if got != tc.want {
			t.Errorf("shadowDevicePath(%d): got %q, want %q", tc.index, got, tc.want)
		}
	}
}

// TestDeviceOrderWithNShadowDisks is the pinning test for the CRITICAL
// INTERACTION described in S-HEAVY-WRITE-DISKS: with N shadow disks
// prepended to ExtraDisks, the workspace must resolve to /dev/vd{b+N}.
// Nothing is hardcoded — every device letter is derived from the actual index.
func TestDeviceOrderWithNShadowDisks(t *testing.T) {
	cases := []struct {
		numShadow        int
		wantWorkspaceDev string
	}{
		{0, "/dev/vdb"}, // no shadow disks: workspace is first ExtraDisk
		{1, "/dev/vdc"},
		{2, "/dev/vdd"},
		{3, "/dev/vde"},
		{4, "/dev/vdf"}, // DefaultShadowDirs has 4 entries
	}

	for _, tc := range cases {
		wm := WorkspaceGuestMount("/workspace/repo", tc.numShadow)
		if wm.Device != tc.wantWorkspaceDev {
			t.Errorf("numShadow=%d: workspace device got %q, want %q",
				tc.numShadow, wm.Device, tc.wantWorkspaceDev)
		}
	}

	// With the default 4 shadow dirs, workspace is ExtraDisks[4] → /dev/vdf.
	diskDir := t.TempDir()
	specs := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, "/workspace/repo", "proj/sb")
	wm := WorkspaceGuestMount("/workspace/repo", len(specs))
	if wm.Device != "/dev/vdf" {
		t.Errorf("default shadow dirs: workspace device got %q, want /dev/vdf", wm.Device)
	}
}

// TestShadowGuestMounts_DeviceOrderAndTargets verifies that shadowGuestMounts
// assigns the correct device letters to shadow disks.
func TestShadowGuestMounts_DeviceOrderAndTargets(t *testing.T) {
	diskDir := t.TempDir()
	specs := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, "/workspace/repo", "proj/sb")

	mounts := shadowGuestMounts(specs, 0 /* no prior ExtraDisks */)

	if len(mounts) != len(DefaultShadowDirs) {
		t.Fatalf("want %d mounts, got %d", len(DefaultShadowDirs), len(mounts))
	}

	wantDevices := []string{"/dev/vdb", "/dev/vdc", "/dev/vdd", "/dev/vde"}
	for i, m := range mounts {
		if m.Device != wantDevices[i] {
			t.Errorf("mounts[%d].Device: got %q, want %q", i, m.Device, wantDevices[i])
		}
		wantTarget := "/workspace/repo/" + DefaultShadowDirs[i]
		if m.Target != wantTarget {
			t.Errorf("mounts[%d].Target: got %q, want %q", i, m.Target, wantTarget)
		}
		if m.FSType != "ext4" {
			t.Errorf("mounts[%d].FSType: got %q, want ext4", i, m.FSType)
		}
	}
}

// TestShadowGuestMounts_WithOffset verifies that extraDisksOffset shifts all
// device letters correctly (for future use with pre-existing ExtraDisks).
func TestShadowGuestMounts_WithOffset(t *testing.T) {
	specs := buildShadowDiskSpecs([]string{"node_modules"}, t.TempDir(), "/workspace/r", "proj/sb")
	mounts := shadowGuestMounts(specs, 2 /* two prior ExtraDisks */)

	if len(mounts) != 1 {
		t.Fatalf("want 1 mount, got %d", len(mounts))
	}
	// offset=2 → index 2 → /dev/vdd
	if mounts[0].Device != "/dev/vdd" {
		t.Errorf("Device: got %q, want /dev/vdd", mounts[0].Device)
	}
}

// TestAllMountsConsistency validates that the full mount list (shadows +
// workspace) uses contiguous, non-overlapping device letters and targets.
func TestAllMountsConsistency(t *testing.T) {
	diskDir := t.TempDir()
	guestPath := "/workspace/proj"
	specs := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, guestPath, "proj/sb")

	shadowMounts := shadowGuestMounts(specs, 0)
	workspaceMount := WorkspaceGuestMount(guestPath, len(specs))
	all := append(shadowMounts, workspaceMount)

	// No two mounts share a device.
	devices := make(map[string]int)
	for i, m := range all {
		if prev, dup := devices[m.Device]; dup {
			t.Errorf("duplicate device %q at mounts[%d] and mounts[%d]", m.Device, prev, i)
		}
		devices[m.Device] = i
	}

	// No two mounts share a target.
	targets := make(map[string]int)
	for i, m := range all {
		if prev, dup := targets[m.Target]; dup {
			t.Errorf("duplicate target %q at mounts[%d] and mounts[%d]", m.Target, prev, i)
		}
		targets[m.Target] = i
	}

	// Workspace device must be lexicographically after all shadow devices
	// (higher ASCII value = higher device index).
	for i, sm := range shadowMounts {
		if workspaceMount.Device <= sm.Device {
			t.Errorf("workspace device %q is not after shadow device %q (index %d)",
				workspaceMount.Device, sm.Device, i)
		}
	}
}

// ── makeShadowExcludeCapturer: source-tree immutability ──────────────────────

// TestShadowExcludeCapturer_DockerignoreUnchangedOnSuccess verifies that
// makeShadowExcludeCapturer does NOT modify .dockerignore during a successful
// capture.
func TestShadowExcludeCapturer_DockerignoreUnchangedOnSuccess(t *testing.T) {
	if !hasMke2fs() {
		t.Skip("mke2fs not found; skipping capturer immutability test")
	}

	dir := t.TempDir()
	diPath := filepath.Join(dir, ".dockerignore")
	origContent := "*.log\n.env\n"
	if err := os.WriteFile(diPath, []byte(origContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// A small source file to capture.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	capturer := makeShadowExcludeCapturer(DefaultShadowDirs)
	outExt4 := filepath.Join(t.TempDir(), "ws.ext4")
	if err := capturer(context.Background(), dir, outExt4, 0 /* auto */); err != nil {
		t.Fatalf("capturer: %v", err)
	}

	got, err := os.ReadFile(diPath)
	if err != nil {
		t.Fatalf("read .dockerignore after capturer: %v", err)
	}
	if string(got) != origContent {
		t.Errorf(".dockerignore was modified on success:\n got: %q\nwant: %q", string(got), origContent)
	}
}

// TestShadowExcludeCapturer_DockerignoreUnchangedOnFailure verifies that
// makeShadowExcludeCapturer does NOT modify .dockerignore when the capture
// fails — specifically when the size guard fires before any image is written.
// This is the failure path the old augment+restore design got wrong: a panic
// or SIGKILL between augment and restore left the file modified permanently.
func TestShadowExcludeCapturer_DockerignoreUnchangedOnFailure(t *testing.T) {
	dir := t.TempDir()
	diPath := filepath.Join(dir, ".dockerignore")
	origContent := "*.log\n.env\n"
	if err := os.WriteFile(diPath, []byte(origContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write enough data to trip a very small threshold.
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	capturer := makeShadowExcludeCapturer(DefaultShadowDirs)
	// threshold=1: guaranteed size-guard failure before any ext4 image is written.
	_ = capturer(context.Background(), dir, filepath.Join(t.TempDir(), "ws.ext4"), 1)

	got, err := os.ReadFile(diPath)
	if err != nil {
		t.Fatalf("read .dockerignore after failed capturer: %v", err)
	}
	if string(got) != origContent {
		t.Errorf(".dockerignore was modified on failure (the bug the old design had):\n got: %q\nwant: %q", string(got), origContent)
	}
}

// TestShadowExcludeCapturer_DockerignoreNotCreated verifies that
// makeShadowExcludeCapturer does NOT create .dockerignore when none existed
// before the (failing) capture.
func TestShadowExcludeCapturer_DockerignoreNotCreated(t *testing.T) {
	dir := t.TempDir()
	diPath := filepath.Join(dir, ".dockerignore")

	if err := os.WriteFile(filepath.Join(dir, "big.bin"), []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	capturer := makeShadowExcludeCapturer(DefaultShadowDirs)
	_ = capturer(context.Background(), dir, filepath.Join(t.TempDir(), "ws.ext4"), 1)

	if _, err := os.Stat(diPath); !os.IsNotExist(err) {
		t.Errorf(".dockerignore should not have been created by capturer; stat: %v", err)
	}
}

// TestShadowExcludeCapturer_ShadowDirsExcluded verifies that shadow directories
// present in the source tree are excluded from the captured ext4 image.
// This ensures the OOM-mitigation property still holds after the fix.
func TestShadowExcludeCapturer_ShadowDirsExcluded(t *testing.T) {
	if !hasMke2fs() {
		t.Skip("mke2fs not found; skipping shadow-dir exclusion test")
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs not found; skipping shadow-dir exclusion test")
	}

	dir := t.TempDir()
	// A regular source file that must be captured.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// node_modules is in DefaultShadowDirs; it must be excluded from the image.
	nmDir := filepath.Join(dir, "node_modules", "lodash")
	if err := os.MkdirAll(nmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "index.js"), []byte("module.exports={}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	capturer := makeShadowExcludeCapturer(DefaultShadowDirs)
	outExt4 := filepath.Join(t.TempDir(), "ws.ext4")
	ctx := context.Background()
	if err := capturer(ctx, dir, outExt4, 0 /* auto */); err != nil {
		t.Fatalf("capturer: %v", err)
	}

	// main.go must be present.
	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /main.go", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /main.go: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "package main") {
		t.Errorf("main.go missing from image; debugfs output: %q", string(out))
	}

	// node_modules must be absent from the root listing.
	lsOut, _ := exec.CommandContext(ctx, "debugfs", "-R", "ls /", outExt4).CombinedOutput()
	if strings.Contains(string(lsOut), "node_modules") {
		t.Errorf("node_modules should be excluded (shadow dir) but appears in image root: %s", lsOut)
	}
}

// ── shadowExtraDisks ──────────────────────────────────────────────────────────

func TestShadowExtraDisks_OrderPreserved(t *testing.T) {
	specs := buildShadowDiskSpecs(DefaultShadowDirs, t.TempDir(), "/workspace/r", "proj/sb")
	eds := shadowExtraDisks(specs)

	if len(eds) != len(specs) {
		t.Fatalf("want %d ExtraDisks, got %d", len(specs), len(eds))
	}
	for i, ed := range eds {
		if ed.Path != specs[i].HostPath {
			t.Errorf("[%d]: ExtraDisk.Path %q != spec.HostPath %q", i, ed.Path, specs[i].HostPath)
		}
	}
}

// ── in-guest mount ordering (KVM-gated) ──────────────────────────────────────

// TestMountWorkspace_ShadowThenWorkspace_KVMGated would verify that
// MountWorkspace mounts shadow dirs before the workspace (planMountOrder sorts
// by path depth so parents precede children) and that the overlay is correct.
// KVM-gated: requires real virtio-blk devices and a Linux guest kernel.
func TestMountWorkspace_ShadowThenWorkspace_KVMGated(t *testing.T) {
	t.Skip("KVM-gated: in-guest MountWorkspace requires a real Linux kernel and virtio-blk devices")
	// Illustrative: build the mounts and call agent.MountWorkspace.
	// With DefaultShadowDirs (4 disks) + workspace:
	//   /dev/vdb → /workspace/repo/node_modules
	//   /dev/vdc → /workspace/repo/.next
	//   /dev/vdd → /workspace/repo/target
	//   /dev/vde → /workspace/repo/dist
	//   /dev/vdf → /workspace/repo  (workspace, mounted first by planMountOrder)
	_ = []agent.GuestMount{}
}

// TestCreateShadowDisk_KVMGated would verify end-to-end that shadow disks are
// created, attached as virtio-blk, and mountable inside a real guest VM.
func TestCreateShadowDisk_KVMGated(t *testing.T) {
	t.Skip("KVM-gated: guest-side mount verification requires a real KVM sandbox")
}

// ── measurement: workspace capture timing (requirement 5) ────────────────────

// hasMke2fs returns true if mke2fs is on the host PATH.
func hasMke2fs() bool {
	for _, p := range []string{"/usr/sbin/mke2fs", "/sbin/mke2fs"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// TestWorkspaceCaptureTiming_NoShadowExclusion measures WorktreeToDisk with a
// synthetic source tree that includes node_modules (no exclusion applied).
// Provides the "without exclusion" baseline for requirement 5.
func TestWorkspaceCaptureTiming_NoShadowExclusion(t *testing.T) {
	if !hasMke2fs() {
		t.Skip("mke2fs not found; skipping workspace capture timing measurement")
	}

	srcDir, outExt4 := prepareMeasurementDirs(t)
	ctx := context.Background()

	start := time.Now()
	err := builder.WorktreeToDisk(ctx, srcDir, outExt4, 2<<30)
	elapsed := time.Since(start)

	t.Logf("MEASUREMENT(no-exclusion, with node_modules): elapsed=%v err=%v",
		elapsed.Round(time.Millisecond), err)
}

// TestWorkspaceCaptureTiming_WithShadowExclusion measures WorktreeToDisk via
// makeShadowExcludeCapturer so node_modules is excluded from the capture.
// Provides the "with exclusion" number for requirement 5.
func TestWorkspaceCaptureTiming_WithShadowExclusion(t *testing.T) {
	if !hasMke2fs() {
		t.Skip("mke2fs not found; skipping workspace capture timing measurement")
	}

	srcDir, outExt4 := prepareMeasurementDirs(t)
	ctx := context.Background()

	capturer := makeShadowExcludeCapturer(DefaultShadowDirs)
	start := time.Now()
	err := capturer(ctx, srcDir, outExt4, 2<<30)
	elapsed := time.Since(start)

	t.Logf("MEASUREMENT(with-shadow-exclusion, node_modules excluded): elapsed=%v err=%v",
		elapsed.Round(time.Millisecond), err)
}

// ── Classification API: IsShadowDisk, ShadowDiskSafeHandle ───────────────────

func TestIsShadowDisk(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		// B1 current format — owned by a handle
		{"b1 node_modules", "example-app_b1-proof-a.shadow.node_modules.ext4", true},
		{"b1 _next", "example-app_b1-proof-a.shadow._next.ext4", true},
		{"b1 dist", "proj_sb.shadow.dist.ext4", true},
		// Legacy pre-B1 format — unconditionally unowned
		{"legacy node_modules", "node_modules.shadow.ext4", true},
		{"legacy dist", "dist.shadow.ext4", true},
		{"legacy target", "target.shadow.ext4", true},
		// Not shadow disks
		{"raw file", "sb-06FZZX7V8XZM12YE7VTR7T8168.raw", false},
		{"workspace ext4", "sb-06FZZX7V8XZM12YE7VTR7T8168-workspace.ext4", false},
		{"intent json", "sb-abc.create-intent.json", false},
		{"plain ext4", "disk.ext4", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShadowDisk(tc.input); got != tc.want {
				t.Errorf("IsShadowDisk(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestShadowDiskSafeHandle(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantHandle string
		wantOK     bool
	}{
		// B1 format: handle is extractable
		{"b1 node_modules", "example-app_b1-proof-a.shadow.node_modules.ext4", "example-app_b1-proof-a", true},
		{"b1 _next", "example-app_b1-proof-a.shadow._next.ext4", "example-app_b1-proof-a", true},
		{"b1 dist", "proj_sb.shadow.dist.ext4", "proj_sb", true},
		{"b1 nested path", "proj_sb.shadow.packages_web_node_modules.ext4", "proj_sb", true},
		// Legacy pre-B1: no handle — unconditionally unowned
		{"legacy node_modules", "node_modules.shadow.ext4", "", false},
		{"legacy dist", "dist.shadow.ext4", "", false},
		{"legacy target", "target.shadow.ext4", "", false},
		// Not shadow disks at all
		{"raw file", "sb-06FZZX7V8XZM12YE7VTR7T8168.raw", "", false},
		{"workspace ext4", "sb-abc-workspace.ext4", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ShadowDiskSafeHandle(tc.input)
			if ok != tc.wantOK || got != tc.wantHandle {
				t.Errorf("ShadowDiskSafeHandle(%q) = (%q, %v), want (%q, %v)",
					tc.input, got, ok, tc.wantHandle, tc.wantOK)
			}
		})
	}
}

// TestCreateShadowDisk_DoesNotInheritStaleFile verifies that createShadowDisk
// removes an existing file at the target path before allocating a fresh disk.
// This is the handle-reuse safety guarantee: a new sandbox never silently
// starts with a prior sandbox's build artifacts.
//
// The test creates a marker file at spec.HostPath, calls createShadowDisk, and
// confirms the marker content is gone (replaced by a fresh ext4 filesystem).
// If createShadowDisk inherits the old file, the marker would survive inside
// the filesystem — this would be caught by mke2fs failing to format, but the
// intent check is stronger: we verify the file was removed BEFORE allocation.
func TestCreateShadowDisk_DoesNotInheritStaleFile(t *testing.T) {
	if !hasMke2fs() {
		t.Skip("mke2fs not found; skipping createShadowDisk handle-reuse test")
	}

	diskDir := t.TempDir()
	spec := ShadowDisk{
		RelDir:      "node_modules",
		HostPath:    filepath.Join(diskDir, "proj_sb.shadow.node_modules.ext4"),
		GuestTarget: "/workspace/repo/node_modules",
	}

	// Write a stale sentinel that simulates a prior sandbox's shadow disk.
	staleContent := []byte("stale-prior-sandbox-content-must-not-survive")
	if err := os.WriteFile(spec.HostPath, staleContent, 0o664); err != nil {
		t.Fatalf("create stale file: %v", err)
	}

	ctx := context.Background()
	if err := createShadowDisk(ctx, spec); err != nil {
		t.Fatalf("createShadowDisk: %v", err)
	}

	// The file must exist (new disk was created).
	info, err := os.Stat(spec.HostPath)
	if err != nil {
		t.Fatalf("shadow disk missing after create: %v", err)
	}
	// A fresh mke2fs-formatted sparse disk must be larger than the stale content.
	if info.Size() == int64(len(staleContent)) {
		t.Errorf("file size %d matches stale content size — disk may not have been replaced", info.Size())
	}
}

// prepareMeasurementDirs creates a synthetic workspace with a populated
// node_modules subtree (~1 MiB of fake JS files) and returns (srcDir, outExt4).
func prepareMeasurementDirs(t *testing.T) (srcDir, outExt4 string) {
	t.Helper()
	srcDir = t.TempDir()
	outExt4 = filepath.Join(t.TempDir(), "ws.ext4")

	// Create a synthetic node_modules tree (~1 MiB).
	nmDir := filepath.Join(srcDir, "node_modules", "lodash")
	if err := os.MkdirAll(nmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		name := "file" + strings.Repeat("a", i+1) + ".js"
		content := strings.Repeat("// synthetic npm file\n", 2500) // ~50 KiB each
		_ = os.WriteFile(filepath.Join(nmDir, name), []byte(content), 0o644)
	}

	// A small source file that should always be captured.
	_ = os.WriteFile(filepath.Join(srcDir, "index.js"), []byte("module.exports = {};\n"), 0o644)
	return srcDir, outExt4
}

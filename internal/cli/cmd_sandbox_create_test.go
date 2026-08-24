package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newBootTestService builds a service backed by a real FileStore in a temp dir
// and the provided driver — similar to newTestService but accepts a driver.
func newBootTestService(t *testing.T, drv driver.Driver) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return service.New(st, drv, lifecycle.New())
}

// putTestImage writes a small fake blob into the given cache and returns the
// resulting domain.Image.
func putTestImage(t *testing.T, cache *image.Cache) domain.Image {
	t.Helper()
	content := []byte("fake-ext4-data")
	h := sha256.New()
	h.Write(content)
	dig := domain.MustDigest(fmt.Sprintf("sha256:%x", h.Sum(nil)))

	img := domain.Image{
		Digest: dig,
		Ref:    "cli-test-base:latest",
		Kind:   domain.KindBase,
	}
	if err := cache.Put(context.Background(), img, bytes.NewReader(content)); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return img
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestSandboxCreate_JSON_StopReason verifies that the sandbox.created JSON
// payload includes a "stop_reason" field (even if empty string is omitted).
// A stopped sandbox should surface its stop_reason in sandbox.list output.
func TestSandboxCreate_JSON_StopReason(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create a sandbox, then stop it, then list and check stop_reason.
	out, _, _ := capture(false)
	if err := runSandboxCreate(ctx, []string{"proj/box"}, out, svc); err != nil {
		t.Fatalf("runSandboxCreate: %v", err)
	}

	// Start requires a substrate, but we can use the fake driver.
	// Start the sandbox (fake driver supports it).
	if _, err := svc.Start(ctx, "proj/box"); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	// Stop it to get a StopReason.
	if _, err := svc.Stop(ctx, "proj/box"); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}

	// List and verify stop_reason appears in JSON.
	listOut, stdout, _ := capture(true)
	if err := runSandboxList(ctx, nil, listOut, svc); err != nil {
		t.Fatalf("runSandboxList: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data: expected object, got %T", env["data"])
	}
	sandboxes, ok := data["sandboxes"].([]any)
	if !ok || len(sandboxes) == 0 {
		t.Fatalf("data.sandboxes: expected non-empty array")
	}
	sb := sandboxes[0].(map[string]any)
	reason, _ := sb["stop_reason"].(string)
	if reason != "clean" {
		t.Errorf("stop_reason = %q, want %q", reason, "clean")
	}
}

// TestSandboxCreate_JSON_StopReason_OmittedWhenRunning verifies that
// stop_reason is omitted from JSON output for a running sandbox (omitempty).
func TestSandboxCreate_JSON_StopReason_OmittedWhenRunning(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	out, _, _ := capture(false)
	if err := runSandboxCreate(ctx, []string{"proj/box"}, out, svc); err != nil {
		t.Fatalf("runSandboxCreate: %v", err)
	}
	if _, err := svc.Start(ctx, "proj/box"); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}

	listOut, stdout, _ := capture(true)
	if err := runSandboxList(ctx, nil, listOut, svc); err != nil {
		t.Fatalf("runSandboxList: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	sandboxes := data["sandboxes"].([]any)
	sb := sandboxes[0].(map[string]any)

	if _, ok := sb["stop_reason"]; ok {
		t.Errorf("stop_reason should be omitted for a running sandbox, got %q", sb["stop_reason"])
	}
}

// TestSandboxCreate_WithImage_CallsStartAndRecordsRunning verifies the boot
// path: given a populated image cache, runSandboxCreate with --image resolves
// the ext4, calls Start (via fake driver), probes reachability, and emits
// sandbox.created with state "running".
func TestSandboxCreate_WithImage_CallsStartAndRecordsRunning(t *testing.T) {
	ctx := context.Background()

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	img := putTestImage(t, cache)

	fd := fake.New()
	svc := newBootTestService(t, fd)

	// Inject the cache root into the CLI via the environment trick OR by using
	// a direct CreateAndBoot call with a custom factory.
	// Since runSandboxCreate constructs its own cache (from store.DefaultRoot),
	// and we cannot override that in a unit test, we call service.CreateAndBoot
	// directly here to cover the boot path — this is the same function wired
	// by runSandboxCreate's boot path.
	probe := func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		gd, ok := drv.(driver.GuestDialer)
		if !ok {
			return nil
		}
		conn, err := gd.DialGuest(ctx, id, driver.AgentControlPort)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}
	newDrv := func(_ string, _ []service.ExtraDisk) (driver.Driver, error) { return fd, nil }

	sb, err := service.CreateAndBoot(ctx, svc, cache, newDrv, probe,
		"proj", "box",
		service.CreateAndBootOptions{
			Image:     service.ImageSpec{Ref: img.Ref},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	if sb.Envelope.ImageDigest != string(img.Digest) {
		t.Errorf("ImageDigest = %q, want %q", sb.Envelope.ImageDigest, img.Digest)
	}

	// Verify list JSON emits the running sandbox.
	listOut, stdout, _ := capture(true)
	if err := runSandboxList(ctx, nil, listOut, svc); err != nil {
		t.Fatalf("runSandboxList: %v", err)
	}
	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	sandboxes := data["sandboxes"].([]any)
	if len(sandboxes) != 1 {
		t.Fatalf("expected 1 sandbox in list, got %d", len(sandboxes))
	}
	sbJSON := sandboxes[0].(map[string]any)
	if sbJSON["state"] != "running" {
		t.Errorf("state = %q, want running", sbJSON["state"])
	}
}

// ── MEMAC1: --memory / --vcpus flag-parse → Config wiring ────────────────────

// TestSandboxCreate_Memory_VCPUs_FlagParsing verifies that parseSandboxCreateArgs
// extracts --memory and --vcpus into the correct fields, and that buildCHConfig
// threads those values into cloudhypervisor.Config.MemoryMiB / .VCPUs.
// Together these two assertions prove the full flag-parse→Config wiring
// without requiring a real VM.
func TestSandboxCreate_Memory_VCPUs_FlagParsing(t *testing.T) {
	args := []string{"p/n", "--image", "nexus3-base:latest", "--memory", "2048", "--vcpus", "2"}
	f, err := parseSandboxCreateArgs(args)
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.memoryMiB != 2048 {
		t.Errorf("memoryMiB: want 2048, got %d", f.memoryMiB)
	}
	if f.vcpus != 2 {
		t.Errorf("vcpus: want 2, got %d", f.vcpus)
	}
	if f.imageRef != "nexus3-base:latest" {
		t.Errorf("imageRef: want %q, got %q", "nexus3-base:latest", f.imageRef)
	}
	if len(f.positionals) != 1 || f.positionals[0] != "p/n" {
		t.Errorf("positionals: want [p/n], got %v", f.positionals)
	}
}

// TestSandboxCreate_Memory_VCPUs_Config verifies that buildCHConfig produces a
// cloudhypervisor.Config with MemoryMiB and VCPUs set correctly, and that zero
// values leave the fields unset (driver keeps its 512 MiB / 1 vCPU defaults).
func TestSandboxCreate_Memory_VCPUs_Config(t *testing.T) {
	cfg := buildCHConfig("/kernel/vmlinux", "/rootfs.ext4", 2048, 2)
	if cfg.MemoryMiB != 2048 {
		t.Errorf("MemoryMiB: want 2048, got %d", cfg.MemoryMiB)
	}
	if cfg.VCPUs != 2 {
		t.Errorf("VCPUs: want 2, got %d", cfg.VCPUs)
	}
	if cfg.KernelPath != "/kernel/vmlinux" {
		t.Errorf("KernelPath: want %q, got %q", "/kernel/vmlinux", cfg.KernelPath)
	}
	if cfg.DiskImagePath != "/rootfs.ext4" {
		t.Errorf("DiskImagePath: want %q, got %q", "/rootfs.ext4", cfg.DiskImagePath)
	}

	// Zero values → fields unset; driver uses its own 512 MiB / 1 vCPU default.
	cfg0 := buildCHConfig("/kernel/vmlinux", "/rootfs.ext4", 0, 0)
	if cfg0.MemoryMiB != 0 {
		t.Errorf("MemoryMiB unset: want 0 (driver default), got %d", cfg0.MemoryMiB)
	}
	if cfg0.VCPUs != 0 {
		t.Errorf("VCPUs unset: want 0 (driver default), got %d", cfg0.VCPUs)
	}
}

// TestSandboxCreate_Memory_VCPUs_InvalidFlag verifies that non-numeric values
// for --memory and --vcpus return UsageErrors.
func TestSandboxCreate_Memory_VCPUs_InvalidFlag(t *testing.T) {
	if _, err := parseSandboxCreateArgs([]string{"p/n", "--memory", "abc"}); err == nil {
		t.Error("expected UsageError for --memory abc, got nil")
	}
	if _, err := parseSandboxCreateArgs([]string{"p/n", "--vcpus", "two"}); err == nil {
		t.Error("expected UsageError for --vcpus two, got nil")
	}
}

// ── S-SURFACE: --motive flag ──────────────────────────────────────────────────

// TestSandboxCreate_Motive_FlagParsing verifies that --motive is parsed into
// sandboxCreateFlags.labels["motive"] and that omitting it leaves the field nil
// (unassociated, preserving existing behaviour).
func TestSandboxCreate_Label_FlagParsing(t *testing.T) {
	// With --label motive=<id>: Labels["motive"] populated.
	args := []string{"p/n", "--image", "nexus3-base:latest", "--label", "motive=m-abc-123"}
	f, err := parseSandboxCreateArgs(args)
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.labels["motive"] != "m-abc-123" {
		t.Errorf("labels[motive]: want %q, got %q", "m-abc-123", f.labels["motive"])
	}
	if f.imageRef != "nexus3-base:latest" {
		t.Errorf("imageRef: want %q, got %q", "nexus3-base:latest", f.imageRef)
	}

	// Multiple --label flags: all keys collected.
	f3, err := parseSandboxCreateArgs([]string{"p/n", "--label", "motive=x", "--label", "env=ci"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs (multi-label): %v", err)
	}
	if f3.labels["motive"] != "x" || f3.labels["env"] != "ci" {
		t.Errorf("multi-label: got %v, want motive=x env=ci", f3.labels)
	}

	// Without --label: Labels stays nil (backwards-compatible default).
	f2, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs (no label): %v", err)
	}
	if len(f2.labels) != 0 {
		t.Errorf("labels without flag: want nil/empty, got %v", f2.labels)
	}
}

// TestSandboxCreate_CaptureMax_FlagParsing verifies that --capture-max parses
// human size strings into int64 byte counts and that omitting the flag yields 0
// (auto free-space-derived guard).
func TestSandboxCreate_CaptureMax_FlagParsing(t *testing.T) {
	// --capture-max 8GiB → 8589934592
	const want8GiB int64 = 8 * 1024 * 1024 * 1024
	f, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest", "--capture-max", "8GiB"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.captureMaxBytes != want8GiB {
		t.Errorf("captureMaxBytes with --capture-max 8GiB: got %d, want %d", f.captureMaxBytes, want8GiB)
	}

	// 500MB → 500_000_000 (decimal SI)
	f2, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest", "--capture-max", "500MB"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs (500MB): %v", err)
	}
	const want500MB int64 = 500_000_000
	if f2.captureMaxBytes != want500MB {
		t.Errorf("captureMaxBytes with --capture-max 500MB: got %d, want %d", f2.captureMaxBytes, want500MB)
	}

	// Omitting --capture-max → 0 (auto)
	f3, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs (no flag): %v", err)
	}
	if f3.captureMaxBytes != 0 {
		t.Errorf("captureMaxBytes without flag: got %d, want 0 (auto)", f3.captureMaxBytes)
	}

	// Invalid value → UsageError
	if _, err := parseSandboxCreateArgs([]string{"p/n", "--capture-max", "notasize"}); err == nil {
		t.Error("expected UsageError for --capture-max notasize, got nil")
	}
}

// TestParseHumanBytes is the comprehensive table test for parseHumanBytes.
//
// Deliberately rejected spellings (case-sensitive suffix matching, no spaces):
//
//	"8gib", "8G", "8 GiB" — wrong case or space before suffix
//	"1e9"                  — scientific notation not supported by ParseInt
//	"-1", "-5GiB"          — negative values are meaningless as a byte cap
//	"0", "0GiB"            — zero is reserved to mean AUTO
//	"NaNGiB", "InfGiB"     — IEEE special values
//	"99999999TiB"          — overflows int64
//
// Valid inputs use exact case-sensitive suffix matching and must be > 0.
func TestParseHumanBytes(t *testing.T) {
	type tc struct {
		in      string
		want    int64 // 0 means "expect error"
		wantErr bool
	}
	tests := []tc{
		// ── valid inputs ─────────────────────────────────────────────────────
		{in: "1024", want: 1024},
		{in: "1", want: 1},
		{in: "1KiB", want: 1024},
		{in: "1MiB", want: 1 << 20},
		{in: "1GiB", want: 1 << 30},
		{in: "8GiB", want: 8 * (1 << 30)},
		{in: "1TiB", want: 1 << 40},
		{in: "1KB", want: 1_000},
		{in: "500MB", want: 500_000_000},
		{in: "1GB", want: 1_000_000_000},

		// ── rejected: negative ───────────────────────────────────────────────
		{in: "-1", wantErr: true},
		{in: "-5GiB", wantErr: true},

		// ── rejected: zero ───────────────────────────────────────────────────
		{in: "0", wantErr: true},
		{in: "0GiB", wantErr: true},

		// ── rejected: empty string ────────────────────────────────────────────
		{in: "", wantErr: true},

		// ── rejected: IEEE special values ─────────────────────────────────────
		{in: "NaNGiB", wantErr: true},
		{in: "InfGiB", wantErr: true},

		// ── rejected: int64 overflow ─────────────────────────────────────────
		{in: "99999999TiB", wantErr: true},

		// ── rejected: wrong case / format (deliberate) ───────────────────────
		{in: "8gib", wantErr: true},  // suffix is not recognised → falls to ParseInt → error
		{in: "8G", wantErr: true},    // no matching suffix
		{in: "8 GiB", wantErr: true}, // space before suffix not matched → ParseFloat("8 ") fails
		{in: "1e9", wantErr: true},   // scientific notation → no suffix match → ParseInt fails
		{in: "notasize", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseHumanBytes(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseHumanBytes(%q) = %d, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHumanBytes(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseHumanBytes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestSandboxCreate_CaptureMax_WorkspacePath proves that parsing --workspace and
// --capture-max together stores captureMaxBytes in the flags struct.
func TestSandboxCreate_CaptureMax_WorkspacePath(t *testing.T) {
	const want8GiB int64 = 8 * 1024 * 1024 * 1024

	dir := t.TempDir() // --workspace requires a real directory that exists
	f, err := parseSandboxCreateArgs([]string{
		"p/n",
		"--image", "nexus3-base:latest",
		"--workspace", dir,
		"--capture-max", "8GiB",
	})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.captureMaxBytes != want8GiB {
		t.Errorf("captureMaxBytes with --workspace + --capture-max 8GiB: got %d, want %d",
			f.captureMaxBytes, want8GiB)
	}
}

// TestSandboxCreate_WorkspaceSpec_CaptureMax verifies the flag→WorkspaceSpec
// handoff through buildWorkspaceSpec, the single production construction site.
//
// This test goes RED if CaptureMaxBytes is removed from buildWorkspaceSpec —
// the seam makes the handoff directly testable without a store or running VM.
// It is the test that the prior TestSandboxCreate_CaptureMax_WorkspacePath
// could not be (that test only exercised flag parsing; its struct literal was
// built by the test itself, not by production code).
func TestSandboxCreate_WorkspaceSpec_CaptureMax(t *testing.T) {
	const want8GiB int64 = 8 * 1024 * 1024 * 1024
	const wsAbs = "/tmp/myproject"
	const guestPath = "/workspace/myproject"

	// Non-zero case: CaptureMaxBytes must flow through buildWorkspaceSpec.
	ws := buildWorkspaceSpec(wsAbs, guestPath, want8GiB)
	if ws.CaptureMaxBytes != want8GiB {
		t.Errorf("CaptureMaxBytes: got %d, want %d", ws.CaptureMaxBytes, want8GiB)
	}
	if ws.SourcePath != wsAbs {
		t.Errorf("SourcePath: got %q, want %q", ws.SourcePath, wsAbs)
	}
	if ws.GuestPath != guestPath {
		t.Errorf("GuestPath: got %q, want %q", ws.GuestPath, guestPath)
	}

	// Zero case: omitted flag → 0 (AUTO mode downstream).
	ws0 := buildWorkspaceSpec(wsAbs, guestPath, 0)
	if ws0.CaptureMaxBytes != 0 {
		t.Errorf("CaptureMaxBytes omitted: got %d, want 0 (auto)", ws0.CaptureMaxBytes)
	}
}

// TestSandboxCreate_WorkspaceCallSite_CaptureMax closes the argument-substitution
// gap left by TestSandboxCreate_WorkspaceSpec_CaptureMax. That test calls
// buildWorkspaceSpec with a literal — proving the constructor populates fields —
// but cannot catch the mutation "replace f.captureMaxBytes with 0 at the call
// site." This test calls workspaceSpecFromFlags with a real sandboxCreateFlags
// struct so it goes RED when f.captureMaxBytes is replaced by a literal 0
// inside workspaceSpecFromFlags (the --memory 8GiB mutation shape).
func TestSandboxCreate_WorkspaceCallSite_CaptureMax(t *testing.T) {
	const want8GiB int64 = 8 * 1024 * 1024 * 1024
	const wsAbs = "/tmp/myproject"
	const guestPath = "/workspace/myproject"

	ws := workspaceSpecFromFlags(sandboxCreateFlags{captureMaxBytes: want8GiB}, wsAbs, guestPath)
	if ws.CaptureMaxBytes != want8GiB {
		t.Errorf("CaptureMaxBytes: got %d, want %d (f.captureMaxBytes not passed through)", ws.CaptureMaxBytes, want8GiB)
	}
	if ws.SourcePath != wsAbs {
		t.Errorf("SourcePath: got %q, want %q", ws.SourcePath, wsAbs)
	}
	if ws.GuestPath != guestPath {
		t.Errorf("GuestPath: got %q, want %q", ws.GuestPath, guestPath)
	}

	// Zero case: default flags → 0 (AUTO mode downstream).
	ws0 := workspaceSpecFromFlags(sandboxCreateFlags{}, wsAbs, guestPath)
	if ws0.CaptureMaxBytes != 0 {
		t.Errorf("CaptureMaxBytes default: got %d, want 0 (auto)", ws0.CaptureMaxBytes)
	}
}

// TestSandboxCreate_WorkspaceEntryPoint_WorkspaceSpec drives the real
// runSandboxCreate entry point with --workspace and --capture-max to close the
// two surviving mutations at cmd_sandbox.go:1140:
//
//   - workspaceSpecFromFlags(sandboxCreateFlags{}, wsAbs, guestPath)  [zero flags]
//   - workspaceSpecFromFlags(f, guestPath, wsAbs)                     [arg swap]
//
// Neither the WorkspaceCallSite nor WorkspaceSpec tests catch these because
// they call the helper directly; this test drives the real call site through
// runSandboxCreate so that replacing f with sandboxCreateFlags{} zeroes
// CaptureMaxBytes (mutation 1) and swapping wsAbs/guestPath scrambles
// SourcePath/GuestPath (mutation 2).
//
// Prerequisite: mke2fs must be on the PATH (the --workspace path calls
// createShadowDisk before reaching :1140).  The test skips when mke2fs is
// absent so it does not become a flaky gate on machines without e2fsprogs.
//
// runSandboxCreate is expected to fail (no real rootfs/driver); the test only
// asserts on the WorkspaceSpec captured via testWorkspaceSpecHook before the
// boot fails.
func TestSandboxCreate_WorkspaceEntryPoint_WorkspaceSpec(t *testing.T) {
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs not found on PATH — skipping entry-point workspace test (install e2fsprogs)")
	}

	// Redirect the nexus3 store to a temp dir so shadow disks do not touch
	// the user's real state directory.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Kernel preflight (added 2026-08-15) runs before shadow disk creation.
	// Point NEXUS3_KERNEL_PATH at a placeholder file so the preflight passes
	// and execution reaches the workspace block where testWorkspaceSpecHook fires.
	kernelFile := filepath.Join(t.TempDir(), "vmlinux-x86_64")
	if err := os.WriteFile(kernelFile, []byte("fake-kernel"), 0o600); err != nil {
		t.Fatalf("write fake kernel: %v", err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", kernelFile)

	const want8GiB int64 = 8 * 1024 * 1024 * 1024

	dir := t.TempDir()

	// Capture the WorkspaceSpec set by workspaceSpecFromFlags at the real call
	// site, before service.CreateAndBoot is reached.
	var capturedSpec *service.WorkspaceSpec
	testWorkspaceSpecHook = func(ws *service.WorkspaceSpec) {
		capturedSpec = ws
	}
	defer func() { testWorkspaceSpecHook = nil }()

	svc := newTestService(t)
	out, _, _ := capture(true)
	ctx := context.Background()

	// --rootfs /nonexistent enters the boot path without needing a cached
	// image; resolveExt4 fails inside service.CreateAndBoot (after the hook
	// fires), so we only care that the hook was reached, not that the command
	// succeeds.
	_ = runSandboxCreate(ctx, []string{
		"proj/name",
		"--rootfs", "/nonexistent-for-test.ext4",
		"--workspace", dir,
		"--capture-max", "8GiB",
	}, out, svc)

	if capturedSpec == nil {
		t.Fatal("testWorkspaceSpecHook was not called — workspaceSpecFromFlags at :1140 may not have been reached (check mke2fs / shadow disk creation)")
	}

	// Mutation 1: workspaceSpecFromFlags(sandboxCreateFlags{}, ...) → CaptureMaxBytes=0
	if capturedSpec.CaptureMaxBytes != want8GiB {
		t.Errorf("CaptureMaxBytes: got %d, want %d (f.captureMaxBytes not forwarded from real flags)", capturedSpec.CaptureMaxBytes, want8GiB)
	}

	// Mutation 2: workspaceSpecFromFlags(f, guestPath, wsAbs) → SourcePath and
	// GuestPath swapped.  wsAbs is the absolute form of dir; guestPath is
	// "/workspace/<basename>" — they are distinct strings, so swapping them is
	// observable.
	wantSource, _ := filepath.Abs(dir)
	wantGuest := "/workspace/" + filepath.Base(wantSource)
	if capturedSpec.SourcePath != wantSource {
		t.Errorf("SourcePath: got %q, want %q (possible arg-order swap in workspaceSpecFromFlags call)", capturedSpec.SourcePath, wantSource)
	}
	if capturedSpec.GuestPath != wantGuest {
		t.Errorf("GuestPath: got %q, want %q (possible arg-order swap in workspaceSpecFromFlags call)", capturedSpec.GuestPath, wantGuest)
	}
}

// TestSandboxCreate_Motive_PersistedToSandbox verifies that MotiveID flows from
// CreateAndBootOptions into the returned sandbox record. Mirrors the boot-path
// integration test (TestSandboxCreate_WithImage_CallsStartAndRecordsRunning):
// calls service.CreateAndBoot directly with MotiveID set, then asserts the
// persisted sandbox carries the motive association.
func TestSandboxCreate_Motive_PersistedToSandbox(t *testing.T) {
	ctx := context.Background()

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	img := putTestImage(t, cache)

	fd := fake.New()
	svc := newBootTestService(t, fd)

	probe := func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		gd, ok := drv.(driver.GuestDialer)
		if !ok {
			return nil
		}
		conn, dialErr := gd.DialGuest(ctx, id, driver.AgentControlPort)
		if dialErr != nil {
			return dialErr
		}
		_ = conn.Close()
		return nil
	}
	newDrv := func(_ string, _ []service.ExtraDisk) (driver.Driver, error) { return fd, nil }

	sb, err := service.CreateAndBoot(ctx, svc, cache, newDrv, probe,
		"proj", "motbox",
		service.CreateAndBootOptions{
			Labels:    map[string]string{"motive": "m-abc-123"},
			Image:     service.ImageSpec{Ref: img.Ref},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.Labels["motive"] != "m-abc-123" {
		t.Errorf("Labels[motive] = %q, want %q", sb.Labels["motive"], "m-abc-123")
	}
}

func TestSandboxCreate_SecretFlagParsing(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{
		"p/n", "--image", "base",
		"--secret", "GH_TOKEN@github.com,api.github.com",
		"--secret", "NPM_TOKEN@registry.npmjs.org",
		"--no-builtin-gh",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.noBuiltinGH {
		t.Error("noBuiltinGH = false")
	}
	if len(f.secrets) != 2 || f.secrets[0] != "GH_TOKEN@github.com,api.github.com" {
		t.Errorf("secrets = %v", f.secrets)
	}
}

func TestResolveCreateSecrets_NoBuiltinKeepsExplicit(t *testing.T) {
	f := sandboxCreateFlags{secrets: []string{"NPM_TOKEN@registry.npmjs.org"}, noBuiltinGH: true}
	binds, err := resolveCreateSecrets(context.Background(), f)
	if err != nil {
		t.Fatalf("resolveCreateSecrets: %v", err)
	}
	if len(binds) != 1 || binds[0].Env != "NPM_TOKEN" {
		t.Errorf("binds = %+v", binds)
	}
}

// TestD36_ExplicitGitHubSecretWithoutRepo_Refused verifies the runtime
// D-PD-36 guard in resolveCreateSecrets: an explicit --secret binding that
// covers a GitHub host is refused when --repo is absent.
//
// This covers the open-egress path that the parse-time check misses (the
// parse-time check only fires for --egress closed).
//
// Mutation evidence: remove the `if f.allowedRepo == ""` guard block from
// resolveCreateSecrets → this test fails because no error is returned.
// Restore → passes.
func TestD36_ExplicitGitHubSecretWithoutRepo_Refused(t *testing.T) {
	f := sandboxCreateFlags{
		secrets:     []string{"GH_TOKEN@github.com,api.github.com"},
		noBuiltinGH: true, // skip builtin so only the explicit bind is in play
		allowedRepo: "",
	}
	_, err := resolveCreateSecrets(context.Background(), f)
	if err == nil {
		t.Fatal("want D-PD-36 error for GitHub secret without --repo, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("want *UsageError, got %T: %v", err, err)
	}
}

// TestD36_ExplicitGitHubSecretWithRepo_Allowed verifies that an explicit
// GitHub --secret binding IS accepted when --repo is provided.
func TestD36_ExplicitGitHubSecretWithRepo_Allowed(t *testing.T) {
	f := sandboxCreateFlags{
		secrets:     []string{"GH_TOKEN@github.com,api.github.com"},
		noBuiltinGH: true,
		allowedRepo: "acme/myrepo",
	}
	binds, err := resolveCreateSecrets(context.Background(), f)
	if err != nil {
		t.Fatalf("resolveCreateSecrets: %v (want nil when --repo is set)", err)
	}
	if len(binds) != 1 || binds[0].Env != "GH_TOKEN" {
		t.Errorf("binds = %+v, want one GH_TOKEN bind", binds)
	}
}

// TestD36_NoBuiltinGH_NoGitHubBind_NoRepo_OK verifies that --no-builtin-gh
// with no explicit GitHub secret and no --repo is accepted. There is no GitHub
// token to bound, so D-PD-36 is not triggered.
func TestD36_NoBuiltinGH_NoGitHubBind_NoRepo_OK(t *testing.T) {
	f := sandboxCreateFlags{
		secrets:     []string{"NPM_TOKEN@registry.npmjs.org"},
		noBuiltinGH: true,
		allowedRepo: "",
	}
	binds, err := resolveCreateSecrets(context.Background(), f)
	if err != nil {
		t.Fatalf("resolveCreateSecrets: %v", err)
	}
	if len(binds) != 1 || binds[0].Env != "NPM_TOKEN" {
		t.Errorf("binds = %+v, want one NPM_TOKEN bind", binds)
	}
}

// ── D-PD-36: --repo flag parse → AllowedRepo wiring ─────────────────────────

// TestD36_RepoFlagParsed verifies that --repo owner/name is parsed into
// sandboxCreateFlags.allowedRepo and round-trips correctly.
//
// Mutation evidence: remove the "--repo" case from parseSandboxCreateArgs →
// this test fails because allowedRepo is empty. Restore → passes.
func TestD36_RepoFlagParsed(t *testing.T) {
	args := []string{"p/n", "--image", "nexus3-base:latest",
		"--egress", "closed", "--repo", "acme/myrepo"}
	f, err := parseSandboxCreateArgs(args)
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.allowedRepo != "acme/myrepo" {
		t.Errorf("allowedRepo: want %q, got %q", "acme/myrepo", f.allowedRepo)
	}
	if !f.egressClosed {
		t.Error("egressClosed: want true, got false")
	}
}

// TestD36_EgressClosedWithoutRepo verifies that --egress closed without --repo
// is rejected at parse time with a clear error (D-PD-36 enforcement).
//
// This is the primary security invariant: a sandbox with GitHub in SecretHosts
// and no path restriction must not be constructible.
//
// Mutation evidence: remove the `if f.egressClosed && f.allowedRepo == ""` block
// from parseSandboxCreateArgs → this test fails because no error is returned.
// Restore → test passes.
func TestD36_EgressClosedWithoutRepo_RefusedAtParse(t *testing.T) {
	_, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest",
		"--egress", "closed"})
	if err == nil {
		t.Fatal("want error for --egress closed without --repo, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("want *UsageError, got %T: %v", err, err)
	}
}

// TestD36_RepoMalformedFlag verifies that --repo without a slash is rejected.
func TestD36_RepoMalformedFlag(t *testing.T) {
	_, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest",
		"--egress", "closed", "--repo", "notaslash"})
	if err == nil {
		t.Fatal("want error for --repo without slash, got nil")
	}
}

// TestD36_RepoFlagMissingValue verifies that --repo with no argument is rejected.
func TestD36_RepoFlagMissingValue(t *testing.T) {
	_, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest",
		"--egress", "closed", "--repo"})
	if err == nil {
		t.Fatal("want error for --repo with no value, got nil")
	}
}

// TestD36_OpenEgressNoRepo_Allowed verifies that an open-egress sandbox
// (--egress open, the default) does NOT require --repo. Open-egress sandboxes
// use AllowAll and are human-interactive; path restriction is not applicable.
func TestD36_OpenEgressNoRepo_Allowed(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.allowedRepo != "" {
		t.Errorf("allowedRepo: want empty for open-egress sandbox, got %q", f.allowedRepo)
	}
	if f.egressClosed {
		t.Error("egressClosed: want false (default open), got true")
	}
}

// TestD36_AllowedRepoWiredToOptions verifies that --repo is passed through
// parseSandboxCreateArgs into f.allowedRepo and would be assigned to
// CreateAndBootOptions.AllowedRepo (the assignment is at cmd_sandbox.go:AllowedRepo).
//
// End-to-end Envelope wiring (CreateAndBootOptions → Envelope.AllowedRepo) is
// proven by service.TestCreateAndBoot_AllowedRepoReachesEnvelope which drives
// the REAL CreateAndBoot with a fake driver. Both tests together prove the full
// flag→parse→options→Envelope chain without a real VM.
//
// Mutation evidence: remove the "--repo" case from parseSandboxCreateArgs →
// this test fails because f.allowedRepo is empty. Restore → passes.
func TestD36_AllowedRepoWiredToOptions(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{
		"proj/d36box", "--egress", "closed", "--repo", "acme/myrepo",
	})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	// f.allowedRepo is the value that cmd_sandbox.go assigns to
	// CreateAndBootOptions.AllowedRepo. If this is non-empty, the proxy sees it.
	if f.allowedRepo != "acme/myrepo" {
		t.Errorf("f.allowedRepo = %q, want %q (AllowedRepo would be empty in CreateAndBootOptions)",
			f.allowedRepo, "acme/myrepo")
	}
}

// TestAgentBuiltinGitHubSuppression pins the CLI-side half of D-SHL-05.
//
// The rule "an agent sandbox never carries a GitHub secret" (D-PD-23) was
// implemented in three places: a pre-boot service guard, a post-boot service
// guard, and a CLI suppression of the builtin `gh auth token` bind. D-SHL-05
// reverses that rule for repo-scoped sandboxes. Lifting only the service
// guards left this suppression enforcing the reversed decision, and a live
// run produced an agent sandbox with --repo set, the service layer willing,
// the supervisor ready to seed — and no GitHub credential anywhere, because
// the bind was discarded before it was ever built.
//
// Nothing in the service package could catch that: from its side the caller
// simply passed no secrets. So the assertion belongs here, on the flags.
//
// Mutation: drop the `&& allowedRepo == ""` clause from suppressBuiltinGitHub
// -> the repo-scoped case goes RED.
func TestAgentBuiltinGitHubSuppression(t *testing.T) {
	cases := []struct {
		name         string
		agentName    string
		allowedRepo  string
		wantSuppress bool
	}{
		{
			// The capability D-SHL-05 exists to enable. This is the case that
			// silently failed live.
			name:         "agent with --repo keeps the builtin GitHub bind",
			agentName:    "claude-code",
			allowedRepo:  "owner/repo",
			wantSuppress: false,
		},
		{
			// Convenience, deliberately preserved: an agent sandbox that never
			// asked for GitHub must not be refused a create over a credential
			// the operator did not request.
			name:         "agent without --repo suppresses the builtin",
			agentName:    "claude-code",
			allowedRepo:  "",
			wantSuppress: true,
		},
		{
			name:         "non-agent with --repo keeps the builtin",
			agentName:    "",
			allowedRepo:  "owner/repo",
			wantSuppress: false,
		},
		{
			name:         "non-agent without --repo keeps the builtin",
			agentName:    "",
			allowedRepo:  "",
			wantSuppress: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Calls the production condition. Restating it here instead would
			// assert a copy, and would stay green through any change to the
			// real one — the drift this whole slice exists to close.
			got := suppressBuiltinGitHub(c.agentName, c.allowedRepo)

			if got != c.wantSuppress {
				t.Errorf("suppressBuiltinGitHub(%q, %q) = %v, want %v",
					c.agentName, c.allowedRepo, got, c.wantSuppress)
			}
		})
	}
}

// ── CFG-egress: [egress].allow wiring ─────────────────────────────────────────

// TestSandboxCreate_ConfigEgress_Allow verifies that egress.allow from
// nexus3.yaml is merged into f.allowHosts ADDITIVELY: both config hosts and
// any --allow-host flags reach resolveAgentPosture; neither replaces the other.
//
// Mutation guard: remove the "f.allowHosts = append(f.allowHosts, cfg.Egress.Allow...)"
// line in applyProjectConfig → both sub-tests fail RED (config hosts absent).
func TestSandboxCreate_ConfigEgress_Allow(t *testing.T) {
	// Set up a temp dir that looks like a git repo root with a nexus3.yaml.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	const yaml = "version: 1\negress:\n  allow: [\"registry.example.com\", \"pkg.example.com\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "nexus3.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	t.Run("config hosts appear when no --allow-host given", func(t *testing.T) {
		f := sandboxCreateFlags{}
		if err := applyProjectConfig(&f); err != nil {
			t.Fatalf("applyProjectConfig: %v", err)
		}
		want := map[string]bool{"registry.example.com": false, "pkg.example.com": false}
		for _, h := range f.allowHosts {
			want[h] = true
		}
		for h, found := range want {
			if !found {
				t.Errorf("allowHosts missing config host %q; got: %v", h, f.allowHosts)
			}
		}
	})

	t.Run("flag hosts and config hosts are both present — additive", func(t *testing.T) {
		f := sandboxCreateFlags{allowHosts: []string{"flag.example.com"}}
		if err := applyProjectConfig(&f); err != nil {
			t.Fatalf("applyProjectConfig: %v", err)
		}
		want := map[string]bool{
			"flag.example.com":     false,
			"registry.example.com": false,
			"pkg.example.com":      false,
		}
		for _, h := range f.allowHosts {
			want[h] = true
		}
		for h, found := range want {
			if !found {
				t.Errorf("allowHosts missing host %q; got: %v", h, f.allowHosts)
			}
		}
	})
}

// ── dev-egress-create: open-egress agent posture ──────────────────────────────

// TestDevEgress_ParseGate_AgentPlusOpenEgress_Allowed verifies that
// --agent claude-code combined with --egress open is no longer refused at
// parse time (D-PD-33 relaxed: dev-egress posture).
//
// Mutation guard: restore the removed parse gate → this test fails RED.
func TestDevEgress_ParseGate_AgentPlusOpenEgress_Allowed(t *testing.T) {
	args := []string{
		"myproject/mysandbox",
		"--agent", "claude-code",
		"--egress", "open",
	}
	_, err := parseSandboxCreateArgs(args)
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: unexpected error for --agent+--egress open: %v", err)
	}
}

// TestDevEgress_ResolveAgentPosture_ClosedByDefault verifies that an agent
// sandbox WITHOUT an explicit --egress flag still gets closed egress
// (openEgress=false). The default MUST be unchanged.
//
// Mutation guard: change `f.egressExplicit && !f.egressClosed` to `!f.egressClosed`
// → openEgress becomes true when no --egress flag is given → this test fails RED.
func TestDevEgress_ResolveAgentPosture_ClosedByDefault(t *testing.T) {
	f := sandboxCreateFlags{agentName: "claude-code"}
	// egressExplicit=false (flag not passed), egressClosed=false (default open)
	_, _, openEgress := resolveAgentPosture(f)
	if openEgress {
		t.Error("resolveAgentPosture: openEgress = true for default agent (no --egress flag); want false")
	}
}

// TestDevEgress_ResolveAgentPosture_OpenWhenExplicit verifies that an agent
// sandbox WITH --egress open gets open egress (openEgress=true).
//
// Mutation guard: change `f.egressExplicit && !f.egressClosed` to `false`
// → openEgress is never true → this test fails RED.
func TestDevEgress_ResolveAgentPosture_OpenWhenExplicit(t *testing.T) {
	f := sandboxCreateFlags{
		agentName:      "claude-code",
		egressExplicit: true,
		egressClosed:   false, // --egress open
	}
	_, _, openEgress := resolveAgentPosture(f)
	if !openEgress {
		t.Error("resolveAgentPosture: openEgress = false for --agent + --egress open; want true")
	}
}

// TestDevEgress_ResolveAgentPosture_ExplicitClosedStaysClosed verifies that
// --agent + --egress closed still produces closed egress.
func TestDevEgress_ResolveAgentPosture_ExplicitClosedStaysClosed(t *testing.T) {
	f := sandboxCreateFlags{
		agentName:      "claude-code",
		egressExplicit: true,
		egressClosed:   true, // --egress closed
	}
	_, _, openEgress := resolveAgentPosture(f)
	if openEgress {
		t.Error("resolveAgentPosture: openEgress = true for --agent + --egress closed; want false")
	}
}

// TestDevEgress_AgentDevEgressSecretHosts_OpenEgress verifies that
// agentDevEgressSecretHosts returns the agent's CredentialedHost when
// openEgress is true, so the MITM proxy can intercept it under AllowAll.
//
// Mutation guard: change the function to return nil → this test fails RED.
func TestDevEgress_AgentDevEgressSecretHosts_OpenEgress(t *testing.T) {
	profile := cred.ClaudeCodeProfile
	hosts := agentDevEgressSecretHosts(profile, true)
	if len(hosts) == 0 {
		t.Fatal("agentDevEgressSecretHosts: got empty slice for open-egress agent; want CredentialedHost")
	}
	if hosts[0] != profile.CredentialedHost {
		t.Errorf("agentDevEgressSecretHosts[0] = %q, want %q", hosts[0], profile.CredentialedHost)
	}
}

// TestDevEgress_AgentDevEgressSecretHosts_ClosedEgress verifies that
// agentDevEgressSecretHosts returns nil when openEgress is false (closed-egress
// path must be unchanged: no ExtraSecretHosts injected).
//
// Mutation guard: remove the `!openEgress` guard → this test fails RED.
func TestDevEgress_AgentDevEgressSecretHosts_ClosedEgress(t *testing.T) {
	profile := cred.ClaudeCodeProfile
	hosts := agentDevEgressSecretHosts(profile, false)
	if len(hosts) != 0 {
		t.Errorf("agentDevEgressSecretHosts: got %v for closed-egress; want nil", hosts)
	}
}

// TestDevEgress_AgentDevEgressSecretHosts_NoAgent verifies that
// agentDevEgressSecretHosts returns nil when no agent profile is set
// (non-agent open-egress sandbox must not get ExtraSecretHosts).
func TestDevEgress_AgentDevEgressSecretHosts_NoAgent(t *testing.T) {
	hosts := agentDevEgressSecretHosts(cred.AgentProfile{}, true)
	if len(hosts) != 0 {
		t.Errorf("agentDevEgressSecretHosts: got %v for zero profile; want nil", hosts)
	}
}

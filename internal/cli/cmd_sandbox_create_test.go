package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.secrets) != 2 || f.secrets[0] != "GH_TOKEN@github.com,api.github.com" {
		t.Errorf("secrets = %v", f.secrets)
	}
}

// TestResolveCreateSecrets_RepoAloneAttachesNoGitHubSecret is a regression test
// for D-PDE-02: `sandbox create --repo X` alone must NOT attach any GitHub
// secret. The builtin auto-append has been removed; only an explicit
// --secret GH_TOKEN@... will produce a GitHub bind.
//
// Mutation evidence: if the auto-append block were re-added to
// resolveCreateSecrets, this test fails because binds would be non-empty.
func TestResolveCreateSecrets_RepoAloneAttachesNoGitHubSecret(t *testing.T) {
	f := sandboxCreateFlags{allowedRepo: "owner/repo"} // no --secret flags
	binds, err := resolveCreateSecrets(context.Background(), f)
	if err != nil {
		t.Fatalf("resolveCreateSecrets: %v", err)
	}
	if len(binds) != 0 {
		t.Errorf("want 0 binds for --repo only, got %d: %+v", len(binds), binds)
	}
}

// TestResolveCreateSecrets_ExplicitSecretKept verifies that an explicit
// non-GitHub --secret bind is still passed through unchanged.
func TestResolveCreateSecrets_ExplicitSecretKept(t *testing.T) {
	f := sandboxCreateFlags{secrets: []string{"NPM_TOKEN@registry.npmjs.org"}}
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

// TestD36_NonGitHubSecretNoRepo_OK verifies that a non-GitHub secret without
// --repo is accepted. D-PD-36 is not triggered when no GitHub host is touched.
func TestD36_NonGitHubSecretNoRepo_OK(t *testing.T) {
	f := sandboxCreateFlags{
		secrets:     []string{"NPM_TOKEN@registry.npmjs.org"},
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

// TestResolveCreateSecrets_PathPolicies verifies the D-PD-36 guard change:
// a GitHub secret is ACCEPTED when covered by a path policy (--egress-policy-json
// channel), even when --repo is absent; refused only when NEITHER is present.
func TestResolveCreateSecrets_PathPolicies(t *testing.T) {
	ctx := context.Background()
	pp := domain.EgressPathPolicies{
		"": {"api.github.com": domain.EgressHostPolicy{Paths: []string{"GET /repos/**"}}},
	}

	t.Run("github secret + covering pathPolicies + no --repo → accepted", func(t *testing.T) {
		f := sandboxCreateFlags{
			secrets:      []string{"GH_TOKEN@api.github.com"},
			pathPolicies: pp,
		}
		_, err := resolveCreateSecrets(ctx, f)
		if err != nil {
			t.Errorf("expected nil error; got %v", err)
		}
	})

	t.Run("github secret + no pathPolicies + no --repo → refused (D-PD-36)", func(t *testing.T) {
		f := sandboxCreateFlags{
			secrets: []string{"GH_TOKEN@api.github.com"},
		}
		_, err := resolveCreateSecrets(ctx, f)
		if err == nil {
			t.Error("expected D-PD-36 error; got nil")
		}
	})

	t.Run("github secret + allowedRepo + no pathPolicies → accepted (shim)", func(t *testing.T) {
		f := sandboxCreateFlags{
			secrets:     []string{"GH_TOKEN@github.com"},
			allowedRepo: "owner/repo",
		}
		_, err := resolveCreateSecrets(ctx, f)
		if err != nil {
			t.Errorf("expected nil error; got %v", err)
		}
	})

	t.Run("non-github secret + no policy + no --repo → accepted", func(t *testing.T) {
		f := sandboxCreateFlags{
			secrets: []string{"GITLAB_TOKEN@gitlab.com"},
		}
		_, err := resolveCreateSecrets(ctx, f)
		if err != nil {
			t.Errorf("expected nil error for non-github secret; got %v", err)
		}
	})
}

// TestParseSandboxCreateArgs_EgressPolicyJSON verifies that --egress-policy-json
// round-trips EgressPathPolicies into sandboxCreateFlags.pathPolicies.
func TestParseSandboxCreateArgs_EgressPolicyJSON(t *testing.T) {
	pp := domain.EgressPathPolicies{
		"": {"api.github.com": domain.EgressHostPolicy{Paths: []string{"GET /repos/**"}}},
	}
	ppJSON, err := json.Marshal(pp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f, err := parseSandboxCreateArgs([]string{"--image", "myimage", "--egress-policy-json", string(ppJSON), "proj/name"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if !reflect.DeepEqual(f.pathPolicies, pp) {
		t.Errorf("pathPolicies round-trip mismatch:\n  want %#v\n   got %#v", pp, f.pathPolicies)
	}
}

// ── D-PD-36-BYPASS regression tests ──────────────────────────────────────────
//
// The BELT and guard tests below are the regression suite for the sole-bound
// bypass: a PathPolicies map with a non-"" top-level key (e.g. "x") looks
// like a covering policy to an all-keys guard but is NEVER enforced by
// lookupPolicy (which consults pp[placeholder] then pp[""]). The real
// placeholder is minted AFTER PathPolicies is frozen at create time, so ""
// is the only key enforcement will ever honour from a user-supplied map.

// TestBelt_EgressPolicyJSON_BogusKey_Refused verifies the BELT (parse-time
// rejection): --egress-policy-json with any non-"" top-level key is refused
// as a usage error before reaching any guard.
//
// Mutation evidence: remove the for-range key check in parseSandboxCreateArgs →
// parseSandboxCreateArgs returns nil error and the bogus-key policy silently
// reaches resolveCreateSecrets, where the pre-fix guard would have accepted it.
func TestBelt_EgressPolicyJSON_BogusKey_Refused(t *testing.T) {
	// "x" is a non-"" key: never enforceable at create time.
	pp := domain.EgressPathPolicies{
		"x": {"api.github.com": domain.EgressHostPolicy{Paths: []string{"/**"}}},
	}
	ppJSON, err := json.Marshal(pp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = parseSandboxCreateArgs([]string{
		"--image", "myimage",
		"--egress-policy-json", string(ppJSON),
		"proj/name",
	})
	if err == nil {
		t.Fatal("expected usage error for bogus-key --egress-policy-json; got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UsageError; got %T: %v", err, err)
	}
}

// TestGuard_BogusKeyGitHubSecret_Refused verifies the guard fix at
// resolveCreateSecrets: a GitHub secret with a PathPolicies map whose ONLY
// entry is under a non-"" key is refused as unbounded (D-PD-36).
//
// NOTE: This test bypasses the BELT (parseSandboxCreateArgs) by constructing
// sandboxCreateFlags directly — it exercises the guard predicate githubHostBoundCLI
// in isolation to prove the predicate fix is correct independent of the BELT.
//
// Mutation evidence: revert githubHostBoundCLI to an all-keys loop →
// resolveCreateSecrets returns nil (bogus-key policy appears to cover the host)
// and this test fails.
func TestGuard_BogusKeyGitHubSecret_Refused(t *testing.T) {
	ctx := context.Background()
	// "x" key: never reached by lookupPolicy at enforcement time.
	pp := domain.EgressPathPolicies{
		"x": {"api.github.com": domain.EgressHostPolicy{Paths: []string{"/**"}}},
	}
	f := sandboxCreateFlags{
		secrets:      []string{"GH_TOKEN@api.github.com"},
		pathPolicies: pp,
		// allowedRepo: "",  // not set
	}
	_, err := resolveCreateSecrets(ctx, f)
	if err == nil {
		t.Fatal("expected D-PD-36 refusal for bogus-key PathPolicies; got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UsageError; got %T: %v", err, err)
	}
}

// TestGuard_WildcardKeyGitHubSecret_Accepted verifies the positive ("" key)
// path still works: a PathPolicies entry under "" covers the host and the
// github secret is accepted without --repo.
//
// Mutation evidence: change githubHostBoundCLI to only accept allowedRepo
// (removing the pp[""] check) → resolveCreateSecrets refuses this valid
// configuration.
func TestGuard_WildcardKeyGitHubSecret_Accepted(t *testing.T) {
	ctx := context.Background()
	pp := domain.EgressPathPolicies{
		"": {"api.github.com": domain.EgressHostPolicy{Paths: []string{"GET /repos/**"}}},
	}
	f := sandboxCreateFlags{
		secrets:      []string{"GH_TOKEN@api.github.com"},
		pathPolicies: pp,
	}
	_, err := resolveCreateSecrets(ctx, f)
	if err != nil {
		t.Fatalf("expected nil for wildcard-key PathPolicies; got %v", err)
	}
}

// ── S16: credential preflight at sandbox create time ─────────────────────────
//
// These tests cover AC-1..AC-6: absent, expired, Claude unaffected, mutation
// proof, and no-token-leak.  All tests use the no-boot (store-only) path so
// they do not require a real VM or kernel.

// cursorCredDir points the Cursor profile's XDG_CONFIG_HOME to dir and
// returns the modified profile.  Uses t.Setenv so the env var is restored
// after the test.
func cursorCredDir(t *testing.T, dir string) cred.AgentProfile {
	t.Helper()
	p := cred.CursorAgentProfile
	t.Setenv(p.CredDirEnvVar, dir)
	return p
}

// writeTestAuthJSON writes a minimal Cursor auth.json with the given
// accessToken to <dir>/cursor/auth.json.
func writeTestAuthJSON(t *testing.T, dir, token string) {
	t.Helper()
	credDir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"),
		[]byte(`{"accessToken":"`+token+`","refreshToken":""}`), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

// expiredJWTToken — exp = 2001-09-09 (genuinely past).
const expiredJWTToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjEwMDAwMDAwMDB9.ZmFrZQ"

// AC-1 (absent): missing cursor credential causes create to fail before a VM boots.
func TestSandboxCreate_CredPreflight_Absent(t *testing.T) {
	dir := t.TempDir()
	cursorCredDir(t, dir) // points XDG_CONFIG_HOME at empty dir — no auth.json
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runSandboxCreate(context.Background(), []string{"--agent", "cursor", "proj/box"}, out, svc)

	// AC-1: must error.
	if err == nil {
		t.Fatal("expected error for absent cursor credential, got nil")
	}
	// AC-1: error code must be bad_credential.
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != sandboxErrCodeBadCredential {
		t.Fatalf("want bad_credential CodedError, got %T %v", err, err)
	}
	// AC-1: message must name the agent.
	if !strings.Contains(coded.Msg, "cursor") {
		t.Fatalf("error message does not name agent: %q", coded.Msg)
	}
	// AC-2 (no-boot leaves no store record): the create failed, no sandbox in store.
	listOut, stdout, _ := capture(true)
	if lErr := runSandboxList(context.Background(), nil, listOut, svc); lErr != nil {
		t.Fatalf("list: %v", lErr)
	}
	var env map[string]any
	dec := json.NewDecoder(stdout)
	if dErr := dec.Decode(&env); dErr != nil {
		t.Fatalf("decode list: %v", dErr)
	}
	data := env["data"].(map[string]any)
	sandboxes := data["sandboxes"].([]any)
	if len(sandboxes) != 0 {
		t.Fatalf("expected 0 sandboxes after failed create, got %d", len(sandboxes))
	}
}

// AC-4 (expired): expired cursor credential causes create to fail with the expired reason.
func TestSandboxCreate_CredPreflight_Expired(t *testing.T) {
	dir := t.TempDir()
	cursorCredDir(t, dir)
	writeTestAuthJSON(t, dir, expiredJWTToken) // JWT with exp=1000000000 (2001)
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runSandboxCreate(context.Background(), []string{"--agent", "cursor", "proj/box"}, out, svc)

	if err == nil {
		t.Fatal("expected error for expired cursor credential, got nil")
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != sandboxErrCodeBadCredential {
		t.Fatalf("want bad_credential CodedError, got %T %v", err, err)
	}
	// The message must mention "expired", not "absent".
	if !strings.Contains(coded.Msg, "expired") {
		t.Fatalf("error message should say 'expired' for expired cred, got: %q", coded.Msg)
	}
}

// AC-3 (Claude unaffected): Claude Code has CredentialFormatNone and must not
// be treated as broken when no credential file exists.
func TestSandboxCreate_CredPreflight_ClaudeCodeUnaffected(t *testing.T) {
	// Use an empty dir so no claude credential file is present.
	dir := t.TempDir()
	t.Setenv(cred.ClaudeCodeProfile.CredDirEnvVar, dir)
	svc := newTestService(t)
	out, _, _ := capture(false)

	// No-boot path: no --image/--rootfs/--file, so create is store-only.
	err := runSandboxCreate(context.Background(), []string{"--agent", cred.ClaudeCodeProfileName, "proj/box"}, out, svc)

	if err != nil {
		t.Fatalf("Claude sandbox create must not be rejected by credential preflight: %v", err)
	}
}

// AC-5 unit proof: credPreflightCheck rejects an absent-credential profile.
// This test calls the helper DIRECTLY — it does not prove the helper is wired
// into any call site.  Use TestSandboxCreate_BootPath_CredPreflight_* for
// call-site coverage on the boot path.
func TestSandboxCreate_CredPreflight_MutationProof(t *testing.T) {
	dir := t.TempDir()
	// No auth.json — cursor profile reports absent.
	p := cursorCredDir(t, dir)

	err := credPreflightCheck(p)

	if err == nil {
		t.Fatal("credPreflightCheck must return an error for absent cursor credential")
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != sandboxErrCodeBadCredential {
		t.Fatalf("want bad_credential, got %T %v", err, err)
	}
}

// AC-6 no-token-leak: Sentence() from a credential error must not contain the
// raw token value.
func TestSandboxCreate_CredPreflight_NoTokenLeak(t *testing.T) {
	const sentinel = "tok_SENTINEL_DO_NOT_LEAK"

	dir := t.TempDir()
	p := cursorCredDir(t, dir)
	// Write a file whose accessToken is the sentinel but is not valid JSON
	// (so the read fails — PreflightUnreadable path).
	credSubdir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credSubdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credSubdir, "auth.json"),
		[]byte(`{"accessToken":"`+sentinel+`_BAD_JSON`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := credPreflightCheck(p)
	if err == nil {
		t.Fatal("expected error for unreadable credential")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error message leaks token sentinel: %q", err.Error())
	}
}

// bootPathEnv sets the two env vars needed to get past the boot-path
// preflights that precede credPreflightCheck (line 1943 in cmd_sandbox.go):
//   - NEXUS3_KERNEL_PATH → a placeholder file (resolveKernelPath at line 1339)
//   - XDG_STATE_HOME     → a temp dir        (store.DefaultRoot  at line 1344)
//
// Call this from every boot-path cred test so that execution reaches
// cmd_sandbox.go:1943 without touching real host state or starting a VM.
func bootPathEnv(t *testing.T) {
	t.Helper()
	kernelFile := filepath.Join(t.TempDir(), "vmlinux-x86_64")
	if err := os.WriteFile(kernelFile, []byte("fake-kernel"), 0o600); err != nil {
		t.Fatalf("write fake kernel: %v", err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", kernelFile)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

// TestSandboxCreate_BootPath_CredPreflight_Absent drives runSandboxCreate
// through the BOOT path (--rootfs triggers line 1297 to fall through) with no
// cursor auth.json.  The check at cmd_sandbox.go:1943 must fire and return
// bad_credential before any VM work begins.
//
// Mutation proof: deleting the if-block at cmd_sandbox.go:1943 makes this
// test RED.  Without the check, execution falls through to service.CreateAndBoot,
// which fails when it cannot stat /nonexistent-for-test.ext4.  That error is
// NOT a CodedError with bad_credential, so the second assertion below fires:
// "boot-path: want bad_credential CodedError, got ...".
func TestSandboxCreate_BootPath_CredPreflight_Absent(t *testing.T) {
	bootPathEnv(t)

	dir := t.TempDir()
	cursorCredDir(t, dir) // points XDG_CONFIG_HOME at empty dir — no auth.json
	svc := newTestService(t)
	out, _, _ := capture(false)

	// --rootfs triggers the boot path (line 1297 condition is false).
	// The value need not be a real file; credPreflightCheck fires before
	// service.CreateAndBoot touches the rootfs path.
	err := runSandboxCreate(context.Background(),
		[]string{"--agent", "cursor", "--rootfs", "/nonexistent-for-test.ext4", "proj/box"},
		out, svc)

	if err == nil {
		t.Fatal("boot-path: expected bad_credential error for absent cursor credential, got nil — " +
			"credPreflightCheck at cmd_sandbox.go:1943 may be missing or unreachable")
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != sandboxErrCodeBadCredential {
		t.Fatalf("boot-path: want bad_credential CodedError, got %T %v", err, err)
	}

	// No sandbox must remain in the store after a failed boot-path create.
	listOut, stdout, _ := capture(true)
	if lErr := runSandboxList(context.Background(), nil, listOut, svc); lErr != nil {
		t.Fatalf("list: %v", lErr)
	}
	var env map[string]any
	dec := json.NewDecoder(stdout)
	if dErr := dec.Decode(&env); dErr != nil {
		t.Fatalf("decode list: %v", dErr)
	}
	data := env["data"].(map[string]any)
	sandboxes := data["sandboxes"].([]any)
	if len(sandboxes) != 0 {
		t.Fatalf("boot-path: expected 0 sandboxes after failed create, got %d", len(sandboxes))
	}
}

// TestSandboxCreate_BootPath_CredPreflight_Expired drives runSandboxCreate
// through the boot path with an expired cursor JWT.  The check at
// cmd_sandbox.go:1943 must return bad_credential with "expired" in the message.
func TestSandboxCreate_BootPath_CredPreflight_Expired(t *testing.T) {
	bootPathEnv(t)

	dir := t.TempDir()
	cursorCredDir(t, dir)
	writeTestAuthJSON(t, dir, expiredJWTToken) // JWT with exp=1000000000 (2001)
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runSandboxCreate(context.Background(),
		[]string{"--agent", "cursor", "--rootfs", "/nonexistent-for-test.ext4", "proj/box"},
		out, svc)

	if err == nil {
		t.Fatal("boot-path: expected bad_credential error for expired cursor credential, got nil — " +
			"credPreflightCheck at cmd_sandbox.go:1943 may be missing or unreachable")
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != sandboxErrCodeBadCredential {
		t.Fatalf("boot-path: want bad_credential CodedError, got %T %v", err, err)
	}
	if !strings.Contains(coded.Msg, "expired") {
		t.Fatalf("boot-path: error message should say 'expired' for expired cred, got: %q", coded.Msg)
	}
}

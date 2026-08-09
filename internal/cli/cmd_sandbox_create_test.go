package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
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
	newDrv := func(_ string) (driver.Driver, error) { return fd, nil }

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
// sandboxCreateFlags.motiveID and that omitting it leaves the field empty
// (unassociated, preserving existing behaviour).
func TestSandboxCreate_Motive_FlagParsing(t *testing.T) {
	// With --motive: field populated.
	args := []string{"p/n", "--image", "nexus3-base:latest", "--motive", "m-abc-123"}
	f, err := parseSandboxCreateArgs(args)
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.motiveID != "m-abc-123" {
		t.Errorf("motiveID: want %q, got %q", "m-abc-123", f.motiveID)
	}
	if f.imageRef != "nexus3-base:latest" {
		t.Errorf("imageRef: want %q, got %q", "nexus3-base:latest", f.imageRef)
	}

	// Without --motive: field stays empty (backwards-compatible default).
	f2, err := parseSandboxCreateArgs([]string{"p/n", "--image", "nexus3-base:latest"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs (no motive): %v", err)
	}
	if f2.motiveID != "" {
		t.Errorf("motiveID without flag: want empty, got %q", f2.motiveID)
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
	newDrv := func(_ string) (driver.Driver, error) { return fd, nil }

	sb, err := service.CreateAndBoot(ctx, svc, cache, newDrv, probe,
		"proj", "motbox",
		service.CreateAndBootOptions{
			MotiveID:  "m-abc-123",
			Image:     service.ImageSpec{Ref: img.Ref},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.MotiveID != "m-abc-123" {
		t.Errorf("MotiveID = %q, want %q", sb.MotiveID, "m-abc-123")
	}
}

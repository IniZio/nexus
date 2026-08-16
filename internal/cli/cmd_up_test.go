package cli

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// ── injectable checkDisk stubs ────────────────────────────────────────────────

// alwaysPassDiskCheck always reports sufficient disk space.
func alwaysPassDiskCheck(_ string, count int) (*service.DiskPreflightResult, error) {
	return &service.DiskPreflightResult{
		Count:           count,
		PerSandboxBytes: 1 << 20, // 1 MiB per sandbox
		ProjectedBytes:  int64(count) << 20,
		FreeBytes:       1 << 40, // 1 TiB free
	}, nil
}

// alwaysFailDiskCheck always returns ErrInsufficientDisk.
func alwaysFailDiskCheck(_ string, _ int) (*service.DiskPreflightResult, error) {
	return &service.DiskPreflightResult{
		Count:           1,
		PerSandboxBytes: int64(5) << 30, // 5 GiB estimate
		ProjectedBytes:  int64(5) << 30,
		FreeBytes:       1 << 20, // 1 MiB free
	}, service.ErrInsufficientDisk
}

// ── helpers ───────────────────────────────────────────────────────────────────

// makeSparseWorkspaceDisk creates a sparse file at path that has a 10 GiB
// apparent size but a tiny allocated footprint (one 4 KiB block). It mimics a
// real workspace ext4 image created via Truncate, which is what nexus3 does.
// Returns (allocatedBytes, apparentBytes). Calls t.Skip if the filesystem
// does not support sparse files.
func makeSparseWorkspaceDisk(t *testing.T, path string) (allocated, apparent int64) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Write one block at offset 0 so stat.Blocks > 0.
	if _, err := f.Write(make([]byte, 4096)); err != nil {
		f.Close()
		t.Fatalf("write block: %v", err)
	}
	const apparentSize = int64(10 << 30) // 10 GiB
	if err := f.Truncate(apparentSize); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	f.Close()

	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatalf("stat: %v", err)
	}
	alloc := st.Blocks * 512
	if alloc >= apparentSize {
		t.Skipf("filesystem does not support sparse files (allocated %d >= apparent %d)", alloc, apparentSize)
	}
	t.Logf("sparse file: apparent=%d, allocated=%d (ratio %.0fx)", apparentSize, alloc, float64(apparentSize)/float64(alloc))
	return alloc, apparentSize
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestRunUp_PreflightRefuses verifies that a disk preflight failure:
//   - returns *UsageError with code "insufficient_disk" → exit code 2 (M3-AC2)
//   - does not create any sandboxes
func TestRunUp_PreflightRefuses(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runUpWithSvc(
		context.Background(),
		[]string{"--count", "3"},
		out,
		svc,
		t.TempDir(),
		alwaysFailDiskCheck,
	)

	if err == nil {
		t.Fatal("want error for preflight failure, got nil")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("want *UsageError (exit 2), got %T: %v", err, err)
	}
	if usageErr.Code != upErrCodeInsufficientDisk {
		t.Errorf("error code = %q, want %q", usageErr.Code, upErrCodeInsufficientDisk)
	}

	// No sandboxes must have been created.
	sandboxes, listErr := svc.List(context.Background())
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(sandboxes) != 0 {
		t.Errorf("want 0 sandboxes after preflight refusal, got %d", len(sandboxes))
	}
}

// TestRunUp_PreflightRefuses_SparseImage is the M3-AC3 regression test at the
// CLI/service boundary.
//
// It creates a sparse workspace disk with 10 GiB apparent size and a tiny
// allocated footprint, then calls the REAL service.CheckDiskSpace with 1 GiB
// injected as the free space.
//
//   - A naive apparent-size implementation: estimates 10 GiB per sandbox →
//     projects 10 GiB → refuses (10 GiB > 1 GiB free) → cmd_up returns exit 2.
//   - The correct allocated-size implementation: estimates a few KB per sandbox →
//     projects a few KB → passes (few KB << 1 GiB free) → cmd_up creates the sandbox.
//
// This test would go RED if service.CheckDiskSpace were changed to use
// os.Stat().Size() instead of stat(2).Blocks*512. See also the companion
// TestCheckDiskSpace_SparseImage in internal/core/service/preflight_test.go.
func TestRunUp_PreflightRefuses_SparseImage(t *testing.T) {
	dir := t.TempDir()
	diskPath := dir + "/sb-01ABCDEFGHIJ-workspace.ext4"
	_, apparent := makeSparseWorkspaceDisk(t, diskPath)

	// Set injected free space to 1 GiB: greater than allocated (few KB) but
	// less than apparent (10 GiB). An apparent-size impl would refuse; correct
	// impl must pass.
	const freeBytes = int64(1) << 30
	if freeBytes >= apparent {
		t.Fatalf("test precondition: freeBytes (%d) >= apparent (%d); adjust constants", freeBytes, apparent)
	}

	// Wrap the real service.CheckDiskSpace with controlled free space.
	checkDisk := func(diskDir string, count int) (*service.DiskPreflightResult, error) {
		old := service.DiskStatfs
		service.DiskStatfs = func(_ string) (int64, error) { return freeBytes, nil }
		defer func() { service.DiskStatfs = old }()
		return service.CheckDiskSpace(diskDir, count)
	}

	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runUpWithSvc(
		context.Background(),
		[]string{"--count", "1"},
		out,
		svc,
		dir,
		checkDisk,
	)

	// Must PASS: allocated bytes are far below freeBytes.
	if err != nil {
		t.Errorf("preflight should pass for sparse disk (allocated << 1 GiB free), got: %v", err)
	}
}

// TestRunUp_CreateN verifies that "up --count N" creates exactly N sandboxes
// when the preflight passes (M3-AC1).
func TestRunUp_CreateN(t *testing.T) {
	const n = 3

	svc := newTestService(t)
	out, _, _ := capture(false)

	if err := runUpWithSvc(
		context.Background(),
		[]string{"--count", "3"},
		out,
		svc,
		t.TempDir(),
		alwaysPassDiskCheck,
	); err != nil {
		t.Fatalf("runUpWithSvc: %v", err)
	}

	sandboxes, listErr := svc.List(context.Background())
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(sandboxes) != n {
		t.Errorf("sandbox count = %d, want %d", len(sandboxes), n)
	}
}

// TestRunUp_Labels verifies that every sandbox created by "up" carries the
// labels specified via repeatable --label KEY=VALUE flags (D-PD-21).
func TestRunUp_Labels(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)

	if err := runUpWithSvc(
		context.Background(),
		[]string{"--count", "2", "--label", "env=test", "--label", "team=infra"},
		out,
		svc,
		t.TempDir(),
		alwaysPassDiskCheck,
	); err != nil {
		t.Fatalf("runUpWithSvc: %v", err)
	}

	sandboxes, listErr := svc.List(context.Background())
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(sandboxes) != 2 {
		t.Fatalf("sandbox count = %d, want 2", len(sandboxes))
	}
	for _, sb := range sandboxes {
		if sb.Labels["env"] != "test" {
			t.Errorf("sandbox %s: label env = %q, want %q", sb.Handle(), sb.Labels["env"], "test")
		}
		if sb.Labels["team"] != "infra" {
			t.Errorf("sandbox %s: label team = %q, want %q", sb.Handle(), sb.Labels["team"], "infra")
		}
	}
}

// TestRunUp_PartialFailure_OutcomeTable verifies that runUpWithSvc reports
// per-sandbox outcomes and the overall count/created/failed breakdown in the
// JSON envelope, satisfying M3-AC1 partial-failure tolerance.
func TestRunUp_PartialFailure_OutcomeTable(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	out, stdout, _ := capture(true)
	if err := runUpWithSvc(ctx, []string{"--count", "3", "--project", "myproj"}, out, svc, t.TempDir(), alwaysPassDiskCheck); err != nil {
		t.Fatalf("runUpWithSvc: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data, _ := env["data"].(map[string]any)

	created, _ := data["created"].(float64)
	failed, _ := data["failed"].(float64)
	outcomes, _ := data["outcomes"].([]any)

	if int(created) != 3 {
		t.Errorf("created = %v, want 3", created)
	}
	if int(failed) != 0 {
		t.Errorf("failed = %v, want 0", failed)
	}
	if len(outcomes) != 3 {
		t.Errorf("outcomes len = %d, want 3", len(outcomes))
	}
	for i, raw := range outcomes {
		o, _ := raw.(map[string]any)
		if success, _ := o["success"].(bool); !success {
			t.Errorf("outcomes[%d].success = false, want true", i)
		}
		if id, _ := o["id"].(string); id == "" {
			t.Errorf("outcomes[%d].id is empty", i)
		}
		if handle, _ := o["handle"].(string); handle == "" {
			t.Errorf("outcomes[%d].handle is empty", i)
		}
	}
}

// TestRunUp_JSON_Schema verifies the JSON success envelope shape for "up".
func TestRunUp_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	out, stdout, _ := capture(true)

	if err := runUpWithSvc(
		context.Background(),
		[]string{"--count", "2", "--project", "myproj"},
		out,
		svc,
		t.TempDir(),
		alwaysPassDiskCheck,
	); err != nil {
		t.Fatalf("runUpWithSvc: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if v, ok := env["schema_version"].(float64); !ok || v != 1 {
		t.Errorf("schema_version: got %v, want 1", env["schema_version"])
	}
	if env["kind"] != "up.completed" {
		t.Errorf("kind: got %v, want up.completed", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data: expected object, got %T", env["data"])
	}
	if count, _ := data["count"].(float64); int(count) != 2 {
		t.Errorf("data.count = %v, want 2", data["count"])
	}
}

// TestRunUp_UnknownFlag verifies that an unrecognised flag returns *UsageError.
func TestRunUp_UnknownFlag(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runUpWithSvc(context.Background(), []string{"--unknown-flag"},
		out, svc, t.TempDir(), alwaysPassDiskCheck)

	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("want *UsageError for unknown flag, got %T: %v", err, err)
	}
}

// TestRunUp_CountMustBePositive verifies that --count 0 returns *UsageError.
func TestRunUp_CountMustBePositive(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runUpWithSvc(context.Background(), []string{"--count", "0"},
		out, svc, t.TempDir(), alwaysPassDiskCheck)

	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("want *UsageError for --count 0, got %T: %v", err, err)
	}
}

// TestRunUp_LabelMustBeKeyValue verifies that --label without "=" is a UsageError.
func TestRunUp_LabelMustBeKeyValue(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)

	err := runUpWithSvc(context.Background(), []string{"--label", "notakeyvalue"},
		out, svc, t.TempDir(), alwaysPassDiskCheck)

	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("want *UsageError for malformed --label, got %T: %v", err, err)
	}
}

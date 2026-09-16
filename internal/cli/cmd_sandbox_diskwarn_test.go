package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
)

// diskWarnEnv isolates runSandboxCreate from the host: a fake kernel so the
// booted path is reachable, empty state/config roots, and a PATH with no
// nexus3-agent so the --file branch stops at "read agent binary" right after
// the disk guard (the first step past the guard that touches a real artefact).
func diskWarnEnv(t *testing.T) []string {
	t.Helper()
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(kernel, []byte("not-a-kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", kernel)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, ".nexus"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, ".nexus", "Containerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"proj/diskwarn", "--file", ctxDir}
}

func stubDiskProbe(t *testing.T, rep service.DiskUsageReport) *int {
	t.Helper()
	calls := 0
	prev := sandboxCreateDiskProbe
	sandboxCreateDiskProbe = func(_ context.Context, stateDir string, c *image.Cache, _ string) (service.DiskUsageReport, error) {
		calls++
		if c == nil {
			t.Error("probe received a nil image cache")
		}
		rep.StateDir = stateDir
		return rep, nil
	}
	t.Cleanup(func() { sandboxCreateDiskProbe = prev })
	return &calls
}

const diskWarnFloor = uint64(15) << 30

func TestSandboxCreate_DiskWarn_BelowFloorRefusesBeforeBuild(t *testing.T) {
	args := diskWarnEnv(t)
	calls := stubDiskProbe(t, service.DiskUsageReport{
		Categories: []service.DiskCategory{
			{Name: service.DiskCategoryImageCache, Count: 3, Bytes: 40 << 30},
			{Name: service.DiskCategorySandboxDisks, Count: 2, Bytes: 9 << 30},
		},
		TotalBytes: 49 << 30,
		FreeBytes:  4 << 30,
		FloorBytes: diskWarnFloor,
		BelowFloor: true,
		Hints:      []string{"nexus3 image prune", "nexus3 reap"},
	})
	svc := newTestService(t)
	out, _, stderr := capture(false)

	err := runSandboxCreate(context.Background(), args, out, svc)
	if err == nil {
		t.Fatal("expected non-nil error when free space is below the floor")
	}
	if *calls != 1 {
		t.Fatalf("probe calls = %d; want 1", *calls)
	}
	if !strings.Contains(err.Error(), "below the 15.0 GiB floor") {
		t.Errorf("error should name the floor; got %q", err)
	}
	if strings.Contains(err.Error(), "read agent binary") {
		t.Errorf("create proceeded past the disk guard into the --file build path: %q", err)
	}
	got := stderr.String()
	for _, want := range []string{
		service.DiskCategoryImageCache, "40.0 GiB",
		"Free: 4.0 GiB (floor 15.0 GiB)",
		"below the builder floor",
		"Next: nexus3 image prune; nexus3 reap",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, got)
		}
	}
}

func TestSandboxCreate_DiskWarn_UnderTwiceFloorWarnsAndProceeds(t *testing.T) {
	args := diskWarnEnv(t)
	calls := stubDiskProbe(t, service.DiskUsageReport{
		Categories: []service.DiskCategory{
			{Name: service.DiskCategoryImageCache, Count: 3, Bytes: 12 << 30},
			{Name: service.DiskCategorySandboxDisks, Count: 2, Bytes: 30 << 30},
		},
		FreeBytes:  20 << 30,
		FloorBytes: diskWarnFloor,
	})
	svc := newTestService(t)
	out, _, stderr := capture(false)

	err := runSandboxCreate(context.Background(), args, out, svc)
	if err == nil || !strings.Contains(err.Error(), "read agent binary") {
		t.Fatalf("expected create to proceed past the disk guard and stop at the agent-binary read; got %v", err)
	}
	if *calls != 1 {
		t.Fatalf("probe calls = %d; want 1", *calls)
	}
	got := stderr.String()
	lines := 0
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "warning: sandbox create: free space") {
			lines++
			for _, want := range []string{"20.0 GiB", "15.0 GiB floor", "largest category: sandbox disks (30.0 GiB)"} {
				if !strings.Contains(l, want) {
					t.Errorf("warning missing %q; got %q", want, l)
				}
			}
		}
	}
	if lines != 1 {
		t.Errorf("want exactly one warning line on stderr, got %d:\n%s", lines, got)
	}
}

func TestSandboxCreate_DiskWarn_AboveTwiceFloorSilent(t *testing.T) {
	args := diskWarnEnv(t)
	stubDiskProbe(t, service.DiskUsageReport{
		Categories: []service.DiskCategory{{Name: service.DiskCategoryImageCache, Bytes: 1 << 30}},
		FreeBytes:  40 << 30,
		FloorBytes: diskWarnFloor,
	})
	svc := newTestService(t)
	out, _, stderr := capture(false)

	err := runSandboxCreate(context.Background(), args, out, svc)
	if err == nil || !strings.Contains(err.Error(), "read agent binary") {
		t.Fatalf("expected create to proceed to the agent-binary read; got %v", err)
	}
	if strings.Contains(stderr.String(), "free space") {
		t.Errorf("no disk warning expected above twice the floor; stderr:\n%s", stderr.String())
	}
}

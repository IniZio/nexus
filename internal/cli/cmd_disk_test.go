package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func seedDiskStateDir(t *testing.T) (stateDir string, orphanDisk string) {
	t.Helper()
	stateDir = t.TempDir()
	fs, err := store.NewFileStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	known := domain.NewSandboxID()
	if err := fs.Create(context.Background(), domain.Sandbox{ID: known, Project: "p", Name: "n", State: domain.Stopped}); err != nil {
		t.Fatal(err)
	}
	write := func(rel string, n int) string {
		p := filepath.Join(stateDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte("d"), n), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(filepath.Join("disks", known.String()+".ext4"), 4096)
	orphanDisk = write(filepath.Join("disks", domain.NewSandboxID().String()+".ext4"), 8192)
	write(filepath.Join("caches", "buildkit.ext4"), 4096)
	return stateDir, orphanDisk
}

func TestDiskUsage_JSON_RoundTrips(t *testing.T) {
	stateDir, _ := seedDiskStateDir(t)
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 1 << 30, nil }))
	out, stdout, _ := capture(true)

	if err := runDiskUsage(context.Background(), nil, out, stateDir, ""); err != nil {
		t.Fatalf("disk usage --json: %v", err)
	}
	var env struct {
		SchemaVersion int                     `json:"schema_version"`
		Kind          string                  `json:"kind"`
		Data          service.DiskUsageReport `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout.String())
	}
	if env.SchemaVersion == 0 || env.Kind != "disk.usage" {
		t.Errorf("envelope schema_version=%d kind=%q, want versioned disk.usage", env.SchemaVersion, env.Kind)
	}
	rep := env.Data
	if rep.StateDir != stateDir {
		t.Errorf("state_dir=%q, want %q", rep.StateDir, stateDir)
	}
	names := make([]string, 0, len(rep.Categories))
	for _, c := range rep.Categories {
		names = append(names, c.Name)
	}
	want := []string{"image cache", "builder templates", "sandbox disks", "build caches", "named volumes", "snapshots", "supervisor logs", "other"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("categories=%v, want %v", names, want)
	}
	var disks service.DiskCategory
	for _, c := range rep.Categories {
		if c.Name == "sandbox disks" {
			disks = c
		}
	}
	if disks.Count != 2 || disks.Reclaimable <= 0 || disks.Reclaimable >= disks.Bytes {
		t.Errorf("sandbox disks=%+v, want count 2 and 0 < reclaimable < bytes", disks)
	}
	if !rep.BelowFloor || rep.FreeBytes != 1<<30 {
		t.Errorf("below_floor=%v free=%d, want below floor at 1 GiB free", rep.BelowFloor, rep.FreeBytes)
	}
	if len(rep.Hints) == 0 || rep.Hints[0] != "nexus3 reap" {
		t.Errorf("hints=%v, want nexus3 reap first", rep.Hints)
	}
}

func TestDiskUsage_Human_ShowsTotalsAndFloorWarning(t *testing.T) {
	stateDir, _ := seedDiskStateDir(t)
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 1 << 30, nil }))
	out, stdout, _ := capture(false)

	if err := runDiskUsage(context.Background(), nil, out, stateDir, ""); err != nil {
		t.Fatalf("disk usage: %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"CATEGORY", "RECLAIMABLE", "sandbox disks", "Reclaimable:", "Free: 1.0 GiB (floor 15.0 GiB)", "below the builder floor", "Next: nexus3 reap"} {
		if !strings.Contains(got, want) {
			t.Errorf("human output missing %q:\n%s", want, got)
		}
	}
}

func TestDiskUsage_Human_NoWarningAboveFloor(t *testing.T) {
	stateDir, orphan := seedDiskStateDir(t)
	if err := os.Remove(orphan); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 100 << 30, nil }))
	out, stdout, _ := capture(false)

	if err := runDiskUsage(context.Background(), nil, out, stateDir, ""); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if strings.Contains(got, "below the builder floor") || strings.Contains(got, "Next:") {
		t.Errorf("unexpected warning or hint with nothing reclaimable and space above floor:\n%s", got)
	}
}

func TestDisk_BareAndUnknownSubverbAreUsageErrors(t *testing.T) {
	out, _, _ := capture(false)
	for _, args := range [][]string{nil, {"bogus"}} {
		err := runDisk(context.Background(), args, out)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("disk %v: err=%v, want *UsageError", args, err)
		}
	}
	if err := runDiskUsage(context.Background(), []string{"extra"}, out, t.TempDir(), ""); err == nil {
		t.Error("disk usage extra: want usage error for stray positional")
	}
}

func TestDisk_ExitCodeNonZeroWithoutSubverb(t *testing.T) {
	if code := Run([]string{"disk"}); code == 0 {
		t.Error("nexus3 disk exited 0, want non-zero")
	}
}

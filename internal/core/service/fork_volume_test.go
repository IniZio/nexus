package service_test

// fork_volume_test.go — D-PD-95: Service.Fork refuses kind=disk volumes.
//
// Tests:
//   1. Fork refused when parent has a kind=disk MountedVolumes entry; error names the volume.
//   2. Fork with no volumes succeeds (no regression on the clean path).
//   3. Leak regression: after the refusal, no <childULID>-* file appears under
//      the volumes directory (ForkFrom was never called, so no child copy was written).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// injectDiskVolume writes a sandbox record with a kind=disk MountedVolumes
// entry into the test store so Service.Fork sees a populated field without
// going through the full create pipeline.
func injectDiskVolume(t *testing.T, st *store.FileStore, volName string) domain.Sandbox {
	t.Helper()
	sb := makeSandbox("fork-vol-parent", "test", domain.Running)
	sb.MountedVolumes = []domain.VolumeAttachment{
		{
			Name:      volName,
			GuestPath: "/mnt/data",
			Kind:      string(volumestore.KindDisk),
			ReadOnly:  false,
		},
	}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return sb
}

// TestFork_RefusedWithDiskVolume asserts that Service.Fork returns an error
// when the parent sandbox has an attached kind=disk named volume, and that
// the error names both the sandbox ID and the volume name (D-PD-95).
func TestFork_RefusedWithDiskVolume(t *testing.T) {
	st := newTestStore(t)
	svc := service.New(st, fake.New(), lifecycle.New())

	const volName = "my-data"
	sb := injectDiskVolume(t, st, volName)

	_, err := svc.Fork(context.Background(), sb.ID.String(), 1)
	if err == nil {
		t.Fatal("Fork: expected error for kind=disk volume, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, sb.ID.String()) {
		t.Errorf("error does not name the sandbox ID %q: %s", sb.ID, errStr)
	}
	if !strings.Contains(errStr, volName) {
		t.Errorf("error does not name the volume %q: %s", volName, errStr)
	}
	if !strings.Contains(errStr, "kind=disk") {
		t.Errorf("error does not mention kind=disk: %s", errStr)
	}
}

// TestFork_SucceedsWithNoVolumes asserts that a parent sandbox with no
// MountedVolumes can still be forked — the D-PD-95 guard must not block
// volume-free sandboxes.
func TestFork_SucceedsWithNoVolumes(t *testing.T) {
	svc := newSvc(t)
	c := ctx()

	sb, err := svc.Create(c, "proj", "fork-clean-parent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	children, err := svc.Fork(c, sb.ID.String(), 2)
	if err != nil {
		t.Fatalf("Fork: unexpected error on volume-free parent: %v", err)
	}
	if len(children) != 2 {
		t.Errorf("Fork: expected 2 children, got %d", len(children))
	}
}

// TestFork_DiskVolume_NoLeakUnderVolumesDir is the leak-regression test.
// It asserts that after a kind=disk fork refusal, no ULID-named file appears
// anywhere under the volumes directory — ForkFrom must never have been called.
func TestFork_DiskVolume_NoLeakUnderVolumesDir(t *testing.T) {
	st := newTestStore(t)
	vs := newVolumeStore(t)
	volRoot := vs.Root() // the directory we watch for leaked child files
	svc := service.New(st, fake.New(), lifecycle.New()).WithVolumes(vs)

	const volName = "leak-test-vol"

	// Create the volume record on disk so DiskPath resolves a real path.
	if _, err := vs.Create(context.Background(), volName, volumestore.KindDisk, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	sb := injectDiskVolume(t, st, volName)

	_, forkErr := svc.Fork(context.Background(), sb.ID.String(), 1)
	if forkErr == nil {
		t.Fatal("Fork: expected refusal, got nil error")
	}

	// Walk the entire volumes directory and report any ULID-keyed child file.
	// A leak would manifest as <childULID>-disk.ext4 under volRoot/<volName>/.
	var leaked []string
	_ = filepath.Walk(volRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		// Any file whose basename contains a dash followed by more characters
		// is a candidate child copy (e.g. "sb-01j3...-disk.ext4").
		base := filepath.Base(path)
		if strings.Count(base, "-") >= 2 {
			leaked = append(leaked, path)
		}
		return nil
	})
	if len(leaked) > 0 {
		t.Errorf("fork refusal leaked %d child file(s) under volumes dir; ForkFrom must not be called:\n%s",
			len(leaked), strings.Join(leaked, "\n"))
	}
}

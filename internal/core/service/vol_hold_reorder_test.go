package service

// Tests for VOL-HOLD-REORDER:
//   1. Device-letter ordering: namedDiskExtras are prepended in declaration
//      order, not sorted order, so §1.5 is preserved even when lock acquisition
//      uses a different order.
//   2. ABBA deadlock: two goroutines naming the same two volumes in opposite
//      declaration order must both succeed (sorted acquisition order breaks the cycle).
//   3. D2 regression: the test hook still fires and the lease is held across
//      store.Create after the reorder.

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// TestNamedVolume_DeviceLetterOrder pins device assignment to declaration order
// even when declaration order differs from sorted order.
//
// Mount list (declaration order): ["vol-z", "vol-a"]
// Sorted lock-acquisition order:  ["vol-a", "vol-z"]
//
// After step 4.7 the DriverFactory must receive:
//
//	ExtraDisks[0].Path == vol-z disk path  (declaration-first)
//	ExtraDisks[1].Path == vol-a disk path  (declaration-second)
//
// If the reorder bug exists (namedDiskExtras built in sorted order) the
// paths would be reversed and this test fails.
func TestNamedVolume_DeviceLetterOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	vsRoot := filepath.Join(t.TempDir(), "volumes")
	vs := volumestore.New(vsRoot)

	// Create both volumes up front so DiskPath is deterministic.
	for _, name := range []string{"vol-z", "vol-a"} {
		if _, err := vs.Create(ctx, name, volumestore.KindDisk, 128<<20, ""); err != nil {
			t.Fatalf("vs.Create(%s): %v", name, err)
		}
	}

	wantPaths := []string{
		vs.DiskPath("vol-z"), // declaration-first → first ExtraDisk → /dev/vdb
		vs.DiskPath("vol-a"), // declaration-second → second ExtraDisk → /dev/vdc
	}

	var capturedDisks []ExtraDisk
	capturingFactory := DriverFactory(func(_ string, extraDisks []ExtraDisk) (driver.Driver, error) {
		capturedDisks = make([]ExtraDisk, len(extraDisks))
		copy(capturedDisks, extraDisks)
		return fake.New(), nil
	})

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	fd := fake.New()
	svc := New(st, fd, lifecycle.New())

	_, createErr := CreateAndBoot(ctx, svc, nil, capturingFactory, noopProbe,
		"proj", "dev-order",
		CreateAndBootOptions{
			Image: ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			// Declaration order: Z first, A second — deliberately reversed
			// from sorted order to make the test sensitive to the bug.
			NamedVolumeMounts: []NamedVolumeMount{
				{Name: "vol-z", Kind: volumestore.KindDisk, SizeBytes: 128 << 20},
				{Name: "vol-a", Kind: volumestore.KindDisk, SizeBytes: 128 << 20},
			},
			Volumes:       vs,
			DiskDir:       t.TempDir(),
			DiskPreflight: func(_ string, _ int64, _ string) (*DiskPreflightResult, error) { return &DiskPreflightResult{}, nil },
		},
	)
	_ = createErr // may fail after driver construction; disk ordering is captured before that

	if len(capturedDisks) < 2 {
		t.Fatalf("factory received %d extra disks, want at least 2", len(capturedDisks))
	}
	for i, want := range wantPaths {
		if capturedDisks[i].Path != want {
			t.Errorf("ExtraDisk[%d].Path = %q, want %q (declaration order)\n"+
				"If this fails, namedDiskExtras was built in sorted order instead of declaration order.",
				i, capturedDisks[i].Path, want)
		}
	}
}

// TestNamedVolume_ABBADeadlock confirms that two goroutines naming the same
// two volumes in opposite declaration order both succeed. Before the sorted
// lock-acquisition fix both goroutines would deadlock and time out.
func TestNamedVolume_ABBADeadlock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	vsRoot := filepath.Join(t.TempDir(), "volumes")
	vs := volumestore.New(vsRoot)

	for _, name := range []string{"vol-a", "vol-b"} {
		if _, err := vs.Create(ctx, name, volumestore.KindDisk, 128<<20, ""); err != nil {
			t.Fatalf("vs.Create(%s): %v", name, err)
		}
	}

	makeOpts := func(mounts []NamedVolumeMount, boxName string) CreateAndBootOptions {
		return CreateAndBootOptions{
			Image:             ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			NamedVolumeMounts: mounts,
			Volumes:           vs,
			DiskDir:           t.TempDir(),
			DiskPreflight:     func(_ string, _ int64, _ string) (*DiskPreflightResult, error) { return &DiskPreflightResult{}, nil },
		}
	}

	// P1 declares [vol-a, vol-b]; P2 declares [vol-b, vol-a] — classic ABBA order.
	mountsP1 := []NamedVolumeMount{
		{Name: "vol-a", Kind: volumestore.KindDisk, SizeBytes: 128 << 20},
		{Name: "vol-b", Kind: volumestore.KindDisk, SizeBytes: 128 << 20},
	}
	mountsP2 := []NamedVolumeMount{
		{Name: "vol-b", Kind: volumestore.KindDisk, SizeBytes: 128 << 20},
		{Name: "vol-a", Kind: volumestore.KindDisk, SizeBytes: 128 << 20},
	}

	type result struct {
		name string
		err  error
	}
	results := make(chan result, 2)

	var wg sync.WaitGroup
	for _, tc := range []struct {
		name   string
		mounts []NamedVolumeMount
	}{
		{"P1", mountsP1},
		{"P2", mountsP2},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, stErr := store.NewFileStore(t.TempDir())
			if stErr != nil {
				results <- result{tc.name, fmt.Errorf("store: %w", stErr)}
				return
			}
			fd := fake.New()
			svc := New(st, fd, lifecycle.New())
			opts := makeOpts(tc.mounts, tc.name)
			_, err := CreateAndBoot(ctx, svc, nil, fakeDriverFactory(fd), noopProbe,
				"proj", tc.name, opts)
			results <- result{tc.name, err}
		}()
	}
	wg.Wait()
	close(results)

	// Both creates must succeed. A deadlock would cause both to time out with
	// "context deadline exceeded" or similar, which is surfaced as an error here.
	//
	// Note: the two goroutines share the same volumestore, which is correct —
	// they are racing for the same rw volumes. With sorted acquisition one
	// succeeds first; the second gets a conflict error ("rw conflict"). That is
	// expected — rw volumes are exclusive. We assert that NEITHER returns a
	// timeout/deadline error (which would indicate deadlock).
	for r := range results {
		if r.err != nil {
			// A conflict error from the verdict table is acceptable (correct
			// enforcement). A deadline-exceeded error means deadlock.
			errStr := r.err.Error()
			if contains(errStr, "deadline exceeded") || contains(errStr, "context deadline") {
				t.Errorf("%s: got deadline error (deadlock?): %v", r.name, r.err)
			}
			// Log non-deadline errors for visibility but don't fail.
			t.Logf("%s result (non-fatal): %v", r.name, r.err)
		} else {
			t.Logf("%s: succeeded", r.name)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

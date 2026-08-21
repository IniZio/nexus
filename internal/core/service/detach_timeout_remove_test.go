package service_test

// TestRemove_RecordDeletedEvenWhenDetachTimesOut proves the critical constraint:
// Service.Remove must delete the sandbox record even if the per-volume detach
// times out (because the volume lock is held by a long-running create or rm).
//
// Concretely: the record deletion at store.Delete happens BEFORE the detach
// loop, and detach errors are silently continued (never returned). Holding the
// volume lock from a separate fd (same process, different open file description,
// so flock exclusion applies) causes detachVolumeLocked to return a
// context.DeadlineExceeded error that Remove discards — the record is gone.
//
// This prevents trading a hang (old behaviour: Exclusive(context.Background())
// parked the CLI indefinitely) for an unremovable sandbox (which would be a
// worse regression).

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

func TestRemove_RecordDeletedEvenWhenDetachTimesOut(t *testing.T) {
	bgCtx := context.Background()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	vs := volumestore.New(t.TempDir())
	svc := service.New(st, fake.New(), lifecycle.New()).WithVolumes(vs)

	// Create the named volume.
	volName := "detach-timeout-vol"
	if _, err := vs.Create(bgCtx, volName, volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	// Create a sandbox record in Created state with one MountedVolume.
	sbID := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:      sbID,
		Name:    "timeout-sb",
		Project: "test",
		State:   domain.Created,
		MountedVolumes: []domain.VolumeAttachment{
			{Name: volName, GuestPath: "/mnt/test", Kind: string(volumestore.KindDir)},
		},
	}
	if err := st.Create(bgCtx, sb); err != nil {
		t.Fatalf("st.Create: %v", err)
	}

	// Record the attachment in volume meta.json so Detach has real work to do.
	if err := vs.Attach(bgCtx, volName, sbID.String()); err != nil {
		t.Fatalf("vs.Attach: %v", err)
	}

	// Hold the volume lock from a separate fd: this causes detachVolumeLocked to
	// time out. A separate open() call creates a distinct open file description,
	// so flock exclusion applies even within the same process.
	lockPath := vs.LockPath(volName)
	lockFile, openErr := os.OpenFile(lockPath, os.O_RDWR, 0)
	if openErr != nil {
		t.Fatalf("open volume lock: %v", openErr)
	}
	defer lockFile.Close() //nolint:errcheck
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold volume lock: %v", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Remove with a short deadline. The store.Delete (record removal) happens
	// before the detach loop, so Remove must return nil even though detach times
	// out. Give enough time for the fast pre-detach steps but let detach exhaust
	// the ctx.
	removeCtx, cancel := context.WithTimeout(bgCtx, 500*time.Millisecond)
	defer cancel()

	if err := svc.Remove(removeCtx, sbID.String()); err != nil {
		t.Fatalf("Remove returned error when detach timed out: %v", err)
	}

	// Sandbox record must be gone — this is the invariant.
	_, getErr := st.Get(bgCtx, sbID)
	if !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("sandbox record still exists after Remove (got err=%v); expected ErrNotFound", getErr)
	}
}

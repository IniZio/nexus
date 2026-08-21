package service_test

// TestRemove_DetachBoundedWithoutCallerDeadline drives Service.Remove from a
// root-shaped context that has NO deadline — exactly what the four production
// CLI call sites supply (signal.NotifyContext(context.Background(), ...)).
//
// The volume lock is held by a competing fd so detachVolumeLocked cannot
// acquire it immediately.  The test asserts that Remove still returns within
// a reasonable wall time, proving that Service.Remove imposes its OWN internal
// deadline on the detach acquisition rather than relying on the caller.
//
// Contrast with TestRemove_RecordDeletedEvenWhenDetachTimesOut, which injects
// a 500 ms deadline into the ctx passed to Remove — that test proves the
// record-before-detach ordering, but injecting the deadline means the test
// supplies the property it is meant to verify.  This test does NOT do that.

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

func TestRemove_DetachBoundedWithoutCallerDeadline(t *testing.T) {
	bgCtx := context.Background() // no deadline — mirrors the production CLI root ctx

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	vs := volumestore.New(t.TempDir())
	svc := service.New(st, fake.New(), lifecycle.New()).WithVolumes(vs)

	// Create the named volume.
	volName := "bounded-detach-vol"
	if _, err := vs.Create(bgCtx, volName, volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	// Create a sandbox record with one MountedVolume.
	sbID := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:      sbID,
		Name:    "bounded-detach-sb",
		Project: "test",
		State:   domain.Created,
		MountedVolumes: []domain.VolumeAttachment{
			{Name: volName, GuestPath: "/mnt/test", Kind: string(volumestore.KindDir)},
		},
	}
	if err := st.Create(bgCtx, sb); err != nil {
		t.Fatalf("st.Create: %v", err)
	}

	// Record the attachment so Detach has real work to do.
	if err := vs.Attach(bgCtx, volName, sbID.String()); err != nil {
		t.Fatalf("vs.Attach: %v", err)
	}

	// Hold the volume lock from a separate fd (distinct open file description →
	// flock exclusion applies even within the same process), forcing detach to
	// spin against EWOULDBLOCK.
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

	// Run Remove with the no-deadline root ctx.  Give it a generous outer
	// wall-clock budget (30 s) so the test does not time out from CI latency;
	// if Service.Remove has no internal deadline the flock spin will eat the
	// entire budget and the select below reports "hung".
	done := make(chan error, 1)
	go func() {
		done <- svc.Remove(bgCtx, sbID.String())
	}()

	select {
	case removeErr := <-done:
		// Remove must succeed: record deletion precedes detach, and detach errors
		// are non-fatal.  If it returned an error, the bound fired incorrectly.
		if removeErr != nil {
			t.Errorf("Remove returned unexpected error: %v", removeErr)
		}
		// Sandbox record must be gone.
		_, getErr := st.Get(bgCtx, sbID)
		if !errors.Is(getErr, store.ErrNotFound) {
			t.Errorf("sandbox record still exists after Remove (got err=%v); expected ErrNotFound", getErr)
		}
	case <-time.After(30 * time.Second):
		// Remove hung — Service.Remove has no internal deadline and TryExclusive
		// is spinning forever against the held lock.
		t.Fatal("Remove hung with no-deadline caller ctx: Service.Remove must impose its own internal detach deadline")
	}
}

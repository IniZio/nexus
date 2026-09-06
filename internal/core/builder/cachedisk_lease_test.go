package builder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAcquireCacheDiskSlot_OwnPinDoesNotReadAsBusy is the own-pin-before-probe
// guard (prior art: motive nexus3-builder-supervisor-spawn-race, b4489a5).
//
// flock belongs to the open file description, not to the process, so a second
// open(2)+LOCK_EX|LOCK_NB of a file THIS process already holds fails with
// EWOULDBLOCK and is indistinguishable from another process holding it. A
// supervisor re-acquiring a slot it already owns must not be told the slot is
// busy by its own lease.
//
// Mutation guard: delete the `if p := pinned[lockPath]; p != nil` branch from
// acquireCacheDiskSlot so it probes first → the second Acquire fails RED.
func TestAcquireCacheDiskSlot_OwnPinDoesNotReadAsBusy(t *testing.T) {
	img := filepath.Join(t.TempDir(), "buildkit.ext4")

	first, err := AcquireCacheDiskSlot(img)
	if err != nil {
		t.Fatalf("first AcquireCacheDiskSlot: %v", err)
	}
	second, err := AcquireCacheDiskSlot(img)
	if err != nil {
		t.Fatalf("re-acquiring a lease this process already holds must succeed, got %v", err)
	}

	// One release must NOT drop the lease while a second reference is out:
	// selection (allowOwnPin=false) must still see the slot as taken.
	first.Release()
	if _, err := acquireCacheDiskSlot(img, false); !errors.Is(err, ErrCacheDiskSlotBusy) {
		t.Fatalf("slot went free while a second reference was still held; got %v", err)
	}

	second.Release()
	free, err := acquireCacheDiskSlot(img, false)
	if err != nil {
		t.Fatalf("slot must be free after the last reference was released, got %v", err)
	}
	free.Release()
}

// TestCacheDiskLeaseRelease_NeverUnlinksTheLockFile guards the second hazard
// from the same prior art (95ba583): unlinking a lease file another process
// holds lets the next opener create a FRESH inode and hold "the same" slot at
// the same time, silently destroying mutual exclusion.
//
// Mutation guard: add os.Remove(l.lockPath) to Release → RED.
func TestCacheDiskLeaseRelease_NeverUnlinksTheLockFile(t *testing.T) {
	img := filepath.Join(t.TempDir(), "buildkit.ext4")
	lockPath := CacheDiskLockPath(img)

	lease, err := AcquireCacheDiskSlot(img)
	if err != nil {
		t.Fatalf("AcquireCacheDiskSlot: %v", err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	lease.Release()

	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lease file was unlinked by Release: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lease file inode changed across Release — two holders could now believe they own the slot")
	}
}

// TestAdoptCacheDiskLeaseFD_RefusesAForeignDescriptor proves the inherited-fd
// adoption fails CLOSED. A wrong fd number would otherwise leave a supervisor
// believing it owns a slot it does not — the silent-mismatch class this
// decision exists to end.
//
// Mutation guard: delete the dev/inode comparison → RED.
func TestAdoptCacheDiskLeaseFD_RefusesAForeignDescriptor(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "buildkit.ext4")
	// A descriptor on some OTHER file, as a mis-numbered ExtraFiles entry
	// would produce.
	other, err := os.CreateTemp(dir, "other-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer other.Close()
	// The slot's lock file must exist so the failure is the identity check,
	// not a missing file.
	lease, err := AcquireCacheDiskSlot(img)
	if err != nil {
		t.Fatalf("AcquireCacheDiskSlot: %v", err)
	}
	lease.Release()

	if _, err := AdoptCacheDiskLeaseFD(int(other.Fd()), img); err == nil {
		t.Fatal("AdoptCacheDiskLeaseFD accepted a descriptor for a different file")
	}
}

// TestEncodeDecodeCacheDiskSlots_RoundTrip covers the record encoding an
// adopting supervisor reads back.
func TestEncodeDecodeCacheDiskSlots_RoundTrip(t *testing.T) {
	for _, in := range [][]string{nil, {"/c/buildkit.ext4"}, {"/c/buildkit.ext4", "/c/npm-1.ext4"}} {
		got := DecodeCacheDiskSlots(EncodeCacheDiskSlots(in))
		if len(got) != len(in) {
			t.Fatalf("round trip of %v gave %v", in, got)
		}
		for i := range in {
			if got[i] != in[i] {
				t.Fatalf("round trip of %v gave %v", in, got)
			}
		}
	}
}

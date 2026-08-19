package service

// shadow_intent.go — the handle-keyed in-flight marker for shadow disks
// (TBD-PD-25).
//
// # Why shadow disks need their own intent
//
// The create intent in intent.go is keyed by sandbox ULID, and its lease
// protects every resource the reaper correlates by ULID. Shadow disks are
// correlated by HANDLE, not ULID: they are materialised before CreateAndBoot
// mints the ULID, so their filenames embed <safeHandle> instead
// (buildShadowDiskSpecs). The reaper's in-flight map is ULID-keyed, so even
// passing it to the shadow classifier would not answer the question the
// classifier asks — "is a create for THIS HANDLE running?".
//
// There is a second, independent hole. Shadow disks are created in the CLI
// before CreateAndBoot is entered at all, so for part of the window the create
// intent does not yet exist. A marker that only appears once CreateAndBoot
// starts cannot cover bytes written before it.
//
// Both holes close the same way: a marker keyed by handle, published before
// the first shadow disk is materialised, leased for as long as the create
// runs. Concretely, a concurrent `nexus3 reap --apply` used to see a shadow
// disk whose handle matched no committed record and delete a live sandbox's
// node_modules mid-create.
//
// # Crash safety
//
// The lease is an flock(2) held by the creating process, so it is released by
// the kernel when that process dies for any reason, including SIGKILL and
// power loss. A dead creator therefore can never permanently protect its
// shadow disks — the next reap reclaims them. This is the same primitive and
// the same reasoning as createIntentLease; see its "Why flock" note.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// shadowIntentSuffix is the filename suffix the reaper's resource index
// matches to discover shadow intents. It deliberately does NOT end in .ext4,
// so diskname.IsShadowDisk never classifies an intent as a disk.
const shadowIntentSuffix = ".shadow-intent.json"

// shadowIntent is the durable marker written to
// <diskDir>/<safeHandle>.shadow-intent.json before any shadow disk for that
// handle is materialised.
//
// It is deleted by ReleaseShadowIntent on clean completion, so a surviving
// intent means the creating process died mid-create.
type shadowIntent struct {
	// Handle is the sandbox handle in "project/name" form. Stored unmangled so
	// the file is self-describing; the filename carries the safeHandle form.
	Handle string `json:"handle"`

	// Paths are the absolute host paths of the shadow disks this create is
	// about to materialise. Recorded so a reaper reclaiming a dead create's
	// intent knows exactly which files that create was responsible for,
	// without re-deriving them from a directory scan.
	Paths []string `json:"paths,omitempty"`
}

// SafeHandle converts a "project/name" handle into the filename-safe form
// embedded in shadow disk and shadow intent filenames.
//
// This is the single definition of that mapping for correlation purposes; the
// reaper compares its output against diskname.ShadowDiskSafeHandle.
func SafeHandle(handle string) string {
	return strings.ReplaceAll(handle, "/", "_")
}

// ShadowIntentPath returns the shadow intent path for handle inside diskDir.
func ShadowIntentPath(diskDir, handle string) string {
	return filepath.Join(diskDir, SafeHandle(handle)+shadowIntentSuffix)
}

// ShadowIntentLease is a held shadow intent. Release it when the create window
// closes; until then a concurrent reaper treats every shadow disk carrying
// this handle as in flight.
type ShadowIntentLease struct {
	inner *createIntentLease
}

// Release removes the shadow intent file and drops the lease. It is safe to
// call on a nil lease and safe to call more than once.
//
// Call it only AFTER the sandbox record is committed. Releasing earlier
// reopens exactly the window this type exists to close: the disks would be
// neither leased nor owned, and a reap landing in that instant deletes them.
func (l *ShadowIntentLease) Release() {
	if l == nil {
		return
	}
	l.inner.release()
}

// Path returns the intent file path, or "" for a nil lease.
func (l *ShadowIntentLease) Path() string {
	if l == nil {
		return ""
	}
	return l.inner.Path()
}

// WriteShadowIntent durably publishes a shadow intent for handle and returns
// the lease protecting it. paths are the shadow disk host paths the caller is
// about to create.
//
// It MUST be called before the first shadow disk is materialised — the marker
// cannot protect bytes that were already on disk when it appeared. The caller
// MUST Release the returned lease once the sandbox record is committed.
//
// Passing no paths is not an error but writes no intent and returns a nil
// lease: there is nothing to protect, and an empty intent would only give the
// reaper another file to reason about.
func WriteShadowIntent(diskDir, handle string, paths []string) (*ShadowIntentLease, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		return nil, fmt.Errorf("shadow intent: mkdir %s: %w", diskDir, err)
	}
	data, err := json.Marshal(shadowIntent{Handle: handle, Paths: paths})
	if err != nil {
		return nil, fmt.Errorf("shadow intent: marshal: %w", err)
	}
	inner, err := publishLeasedIntent(diskDir, ShadowIntentPath(diskDir, handle), data, "shadow intent")
	if err != nil {
		return nil, err
	}
	return &ShadowIntentLease{inner: inner}, nil
}

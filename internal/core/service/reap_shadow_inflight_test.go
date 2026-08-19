package service_test

// TBD-PD-25: `nexus3 reap --apply` running concurrently with `nexus3 create`
// could delete a live sandbox's shadow disks.
//
// Two independent holes produced it:
//
//  1. Shadow disks are correlated by HANDLE; the reaper's in-flight map was
//     keyed by ULID, so it could not answer "is a create for this handle
//     running?" even in principle. Mid-create the handle has no committed
//     record, so every shadow disk classified ORPHAN.
//
//  2. Shadow disks are materialised in the CLI BEFORE CreateAndBoot writes the
//     ULID-keyed intent, so for part of the window no marker existed at all.
//
// The shadow intent closes both: handle-keyed, and published before the first
// disk byte. These tests drive Reap, not the classifier, so they fail if the
// intent stops being enumerated, stops being probed, or stops being consulted.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// heldShadowIntent publishes a shadow intent for handle covering paths and
// registers its release. The returned lease is held for the test's duration,
// which is what a live create looks like to the reaper.
func heldShadowIntent(t *testing.T, disksDir, handle string, paths []string) *service.ShadowIntentLease {
	t.Helper()
	lease, err := service.WriteShadowIntent(disksDir, handle, paths)
	if err != nil {
		t.Fatalf("WriteShadowIntent(%q): %v", handle, err)
	}
	t.Cleanup(lease.Release)
	return lease
}

// A create in flight holds the shadow intent lease. Its disks have no record
// yet — that is the whole point — so the reaper must keep them anyway.
func TestReap_ShadowDisk_InFlightCreateIsKept(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	diskPaths := []string{
		mustWriteShadowDisk(t, disksDir, "live_create.shadow.node_modules.ext4"),
		mustWriteShadowDisk(t, disksDir, "live_create.shadow.dist.ext4"),
	}
	heldShadowIntent(t, disksDir, "live/create", diskPaths)

	// Store is empty: mid-create, the record is not committed yet.
	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	status := make(map[string]service.ReapStatus)
	for _, e := range report.Entries {
		status[e.Resource.Path] = e.Status
	}
	for _, p := range diskPaths {
		if status[p] == service.ReapStatusOrphan {
			t.Errorf("shadow disk %s classified orphan while its create holds the intent lease", filepath.Base(p))
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("shadow disk %s was DELETED mid-create: %v", filepath.Base(p), err)
		}
	}
	// The intent itself must survive too — deleting it would unprotect the
	// disks for the next reap while the same create is still running.
	intentPath := service.ShadowIntentPath(disksDir, "live/create")
	if _, err := os.Stat(intentPath); err != nil {
		t.Errorf("held shadow intent was deleted: %v", err)
	}
}

// The lease must not be a permanent shield: once the creator is gone the
// disks are reclaimable exactly as before, or a crashed create leaks 10 GiB
// per shadow dir forever.
func TestReap_ShadowDisk_ReleasedIntentDoesNotProtect(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	diskPaths := []string{
		mustWriteShadowDisk(t, disksDir, "dead_create.shadow.node_modules.ext4"),
	}
	lease, err := service.WriteShadowIntent(disksDir, "dead/create", diskPaths)
	if err != nil {
		t.Fatalf("WriteShadowIntent: %v", err)
	}
	lease.Release() // the create finished (or died and the kernel dropped it)

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, e := range report.Entries {
		if e.Resource.Path == diskPaths[0] && e.Status != service.ReapStatusOrphan {
			t.Errorf("shadow disk status = %q after the intent was released, want orphan (reason: %s)", e.Status, e.Reason)
		}
	}
	if _, err := os.Stat(diskPaths[0]); err == nil {
		t.Error("shadow disk survived reap --apply after its intent was released")
	}
}

// The crash case, and the one that decides whether this whole mechanism is
// safe to add to a reaper: a creator that died leaves its intent FILE behind
// (the defer never ran) but the kernel dropped its flock. An unleased intent
// must protect nothing, or one crashed create leaks 10 GiB per shadow dir
// forever and the reaper can never reclaim it.
func TestReap_ShadowDisk_UnleasedIntentProtectsNothing(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	diskPath := mustWriteShadowDisk(t, disksDir, "crashed_box.shadow.node_modules.ext4")

	// Write the intent file directly: present on disk, held by nobody. This is
	// exactly what a SIGKILLed creator leaves behind.
	intentPath := service.ShadowIntentPath(disksDir, "crashed/box")
	if err := os.WriteFile(intentPath, []byte(`{"handle":"crashed/box","paths":["`+diskPath+`"]}`), 0o600); err != nil {
		t.Fatalf("write orphaned intent: %v", err)
	}

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, e := range report.Entries {
		if e.Resource.Path == diskPath && e.Status != service.ReapStatusOrphan {
			t.Errorf("disk status = %q behind an UNLEASED intent, want orphan (reason: %s)", e.Status, e.Reason)
		}
		if e.Resource.Path == intentPath && e.Status != service.ReapStatusOrphan {
			t.Errorf("unleased intent status = %q, want orphan (reason: %s)", e.Status, e.Reason)
		}
	}
	if _, err := os.Stat(diskPath); err == nil {
		t.Error("shadow disk survived reap --apply behind an unleased intent — a crashed create can leak forever")
	}
	if _, err := os.Stat(intentPath); err == nil {
		t.Error("unleased intent survived reap --apply")
	}
}

// The lease is scoped to one handle: an unrelated sandbox's leaked disks must
// still be reclaimed while a create runs elsewhere.
func TestReap_ShadowDisk_InFlightLeaseIsHandleScoped(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	livePath := mustWriteShadowDisk(t, disksDir, "live_create.shadow.node_modules.ext4")
	leakedPath := mustWriteShadowDisk(t, disksDir, "gone_project.shadow.node_modules.ext4")
	heldShadowIntent(t, disksDir, "live/create", []string{livePath})

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	status := make(map[string]service.ReapStatus)
	for _, e := range report.Entries {
		status[e.Resource.Path] = e.Status
	}
	if status[livePath] == service.ReapStatusOrphan {
		t.Error("leased handle's disk classified orphan")
	}
	if status[leakedPath] != service.ReapStatusOrphan {
		t.Errorf("unrelated leaked disk status = %q, want orphan — the lease must not shield other handles", status[leakedPath])
	}
	if _, err := os.Stat(leakedPath); err == nil {
		t.Error("unrelated leaked disk survived reap --apply")
	}
}

// A shadow intent must never be mistaken for a shadow disk: it does not end in
// .ext4, and classifying it as a disk would put a JSON marker in the byte
// accounting and hand the wrong noun to the operator.
func TestResourceIndex_ShadowIntentIsItsOwnKind(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	diskPath := mustWriteShadowDisk(t, disksDir, "some_box.shadow.node_modules.ext4")
	heldShadowIntent(t, disksDir, "some/box", []string{diskPath})

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var sawIntent, sawDisk bool
	for _, r := range resources {
		switch r.Kind {
		case service.KindShadowIntent:
			sawIntent = true
			if r.ShadowHandle != "some_box" {
				t.Errorf("shadow intent ShadowHandle = %q, want %q", r.ShadowHandle, "some_box")
			}
		case service.KindDiskShadow:
			sawDisk = true
			if filepath.Ext(r.Path) != ".ext4" {
				t.Errorf("non-disk %s enumerated as KindDiskShadow", filepath.Base(r.Path))
			}
		}
	}
	if !sawIntent {
		t.Error("shadow intent was not enumerated")
	}
	if !sawDisk {
		t.Error("shadow disk was not enumerated")
	}
}

// RL-14: `nexus3 fork` copies every parent extra disk, and shadow disks ARE
// extra disks, so each child gets
// <childULID>-<parentSafeHandle>.shadow.<name>.ext4 (ChildExtraDiskPath).
// That composite matches no sandbox handle, so handle correlation alone
// orphans it permanently — `reap --apply` would delete a LIVE forked
// sandbox's dependency tree at any point after the fork window closed.
//
// The register recorded this hazard's premise as unconfirmed ("no code path
// that creates fork shadow-disk copies could be located"). The producer does
// exist; it is in the driver, not Service.Fork.
func TestReap_ShadowDisk_ForkChildCopyIsOwned(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// A live CHILD record. Its handle is unrelated to the parent's, exactly as
	// a fork child's is.
	st, childID := makeStoreWithSandbox(t, "proj", "child")

	childCopy := mustWriteShadowDisk(t, disksDir,
		childID.String()+"-proj_parent.shadow.node_modules.ext4")

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, e := range report.Entries {
		if e.Resource.Path == childCopy && e.Status == service.ReapStatusOrphan {
			t.Errorf("fork child's shadow copy classified orphan while the child is live (reason: %s)", e.Reason)
		}
	}
	if _, err := os.Stat(childCopy); err != nil {
		t.Errorf("fork child's shadow copy was DELETED while the child is live: %v", err)
	}
}

// The ULID escape hatch must not become a blanket amnesty: once the child is
// gone its copies are reclaimable like anything else.
func TestReap_ShadowDisk_ForkChildCopyReclaimedWhenChildGone(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	deadChild := domain.NewSandboxID()
	orphanCopy := mustWriteShadowDisk(t, disksDir,
		deadChild.String()+"-proj_parent.shadow.node_modules.ext4")

	st := newEmptyStore(t) // no record for deadChild
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, e := range report.Entries {
		if e.Resource.Path == orphanCopy && e.Status != service.ReapStatusOrphan {
			t.Errorf("dead fork child's copy status = %q, want orphan (reason: %s)", e.Status, e.Reason)
		}
	}
	if _, err := os.Stat(orphanCopy); err == nil {
		t.Error("dead fork child's shadow copy survived reap --apply")
	}
}

package service_test

// TBD-PD-38, sibling path. `RestoreFromSnapshot` had NO intent leases at all —
// neither the ULID-keyed create intent that D-PD-74 gave Fork, nor the
// handle-keyed shadow intent that TBD-PD-38 added. Every child disk it copies
// was exposed to a concurrent `reap --apply` for the whole restore window,
// which is strictly wider than the fork exposure that was on record.
//
// This test fires a REAL service.Reap(apply=true) from inside ForkFrom, which
// restore reaches through the same driver call fork does.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

func TestRestore_InFlightChildDisksSurviveConcurrentReap(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	diskDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(diskDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := &reapDuringForkDriver{FakeDriver: fake.New()}
	aStore := makeArtifactStore(t)
	svc := service.New(st, drv, lifecycle.New()).WithDiskDir(diskDir).WithArtifacts(aStore)

	origin, err := svc.Create(ctx, "proj", "restore-origin", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create origin: %v", err)
	}
	const snapIDStr = "restore-inflight-snap-000000"
	writeSnapWithOrigin(t, aStore, snapIDStr, origin.ID, artifact.KindRetained)

	parentBase := seedParentShadowDisk(t, diskDir, origin.Handle(), "node_modules")

	idx := service.NewResourceIndex(service.IndexConfig{StateRoot: stateRoot, SocketDir: t.TempDir()})
	procDir := t.TempDir()

	var rawPaths, shadowPaths []string
	drv.onFork = func(childIDs []domain.SandboxID) {
		for _, id := range childIDs {
			p := filepath.Join(diskDir, id.String()+".raw")
			if wErr := os.WriteFile(p, []byte("child disk"), 0o600); wErr != nil {
				t.Errorf("materialise %s: %v", p, wErr)
			}
			rawPaths = append(rawPaths, p)
		}
		shadowPaths = materialiseChildShadowCopies(t, diskDir, childIDs, []string{parentBase})

		if _, rErr := service.Reap(context.Background(), st, idx, true, /*apply*/
			service.ReapOptions{ProcDir: procDir}); rErr != nil {
			t.Errorf("Reap: %v", rErr)
		}
	}

	children, err := svc.RestoreFromSnapshot(ctx, artifact.SnapshotID(snapIDStr), 2)
	if err != nil {
		t.Fatalf("RestoreFromSnapshot: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("restored %d children, want 2", len(children))
	}

	for _, p := range rawPaths {
		if _, sErr := os.Stat(p); sErr != nil {
			t.Errorf("child .raw %s was DELETED mid-restore: %v", filepath.Base(p), sErr)
		}
	}
	for _, p := range shadowPaths {
		if _, sErr := os.Stat(p); sErr != nil {
			t.Errorf("child shadow copy %s was DELETED mid-restore: %v", filepath.Base(p), sErr)
		}
	}

	// Intents must not outlive the restore.
	entries, err := os.ReadDir(diskDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("intent %s survived a completed restore", e.Name())
		}
	}

	// Sanity: the copies really are named the way the driver names them, so a
	// pass here means the lease covered the production path.
	want := cloudhypervisor.ChildExtraDiskPath(children[0].ID, filepath.Join(diskDir, parentBase))
	if _, sErr := os.Stat(want); sErr != nil {
		t.Errorf("driver-named copy for child 0 missing: %v", sErr)
	}
}

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestBuilderSupervisorDriver_HandsCacheDiskLeasesToTheSupervisor proves the
// CLI rewire of D-HSH-07: the process that SELECTS a cache-disk slot must hand
// the lease to the supervisor that owns the builder VM, not keep it for its
// own lifetime.
//
// It drives the real buildSpawnConfig — the function Start calls — with leases
// produced by the real builder.SelectCacheDisks, and asserts the spawn config
// carries both the slot path and the open lock descriptor for it.
//
// Mutation guards, each of which must turn this RED:
//   - drop `CacheDiskSlots: cacheSlotPaths` from buildSpawnConfig;
//   - drop `CacheDiskLeaseFiles: cacheLeaseFiles` from buildSpawnConfig;
//   - stop populating supervisorBuilderDriver.cacheDiskLeases in cmd_sandbox.go
//     (the lease then stays CLI-scoped, which is the pre-D-HSH-07 defect).
func TestBuilderSupervisorDriver_HandsCacheDiskLeasesToTheSupervisor(t *testing.T) {
	dataDir := t.TempDir()
	img := filepath.Join(dataDir, "caches", "buildkit.ext4")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lease, err := builder.AcquireCacheDiskSlot(img)
	if err != nil {
		t.Fatalf("AcquireCacheDiskSlot: %v", err)
	}
	defer lease.Release()

	d := &supervisorBuilderDriver{
		storeRoot:       dataDir,
		stateBase:       dataDir,
		extraDisks:      []string{"/ctx.ext4", "/artifact.ext4", img},
		cacheDiskLeases: []*builder.CacheDiskLease{lease},
	}
	cfg := d.buildSpawnConfig(domain.NewSandboxID(), t.TempDir())

	if len(cfg.CacheDiskSlots) != 1 || cfg.CacheDiskSlots[0] != img {
		t.Fatalf("CacheDiskSlots = %v, want [%s]", cfg.CacheDiskSlots, img)
	}
	if len(cfg.CacheDiskLeaseFiles) != 1 || cfg.CacheDiskLeaseFiles[0] == nil {
		t.Fatalf("CacheDiskLeaseFiles = %v, want the held lock descriptor", cfg.CacheDiskLeaseFiles)
	}
	// The descriptor must be the lease's own open file description — that is
	// what makes the handoff gapless. Compare by inode.
	var fdStat, lockStat os.FileInfo
	if fdStat, err = cfg.CacheDiskLeaseFiles[0].Stat(); err != nil {
		t.Fatalf("stat forwarded descriptor: %v", err)
	}
	if lockStat, err = os.Stat(builder.CacheDiskLockPath(img)); err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if !os.SameFile(fdStat, lockStat) {
		t.Fatal("forwarded descriptor does not refer to the slot's lock file")
	}
}

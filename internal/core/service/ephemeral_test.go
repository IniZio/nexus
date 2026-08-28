package service

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
)

// noDialDriver is a minimal driver.Driver implementation that:
//   - Start succeeds (returns a dummy instance ID)
//   - Stop succeeds
//   - Observe returns Absent
//   - Does NOT implement driver.GuestDialer
//
// The missing GuestDialer causes svc.Exec to fail via agentClientFor (the
// type assertion to driver.GuestDialer returns ok=false → ErrNoSubstrate).
// This is the fault-injection mechanism for TestRunEphemeral_ZeroLeftoversOnFault.
type noDialDriver struct{}

func (d *noDialDriver) Name() string { return "no-dial" }

func (d *noDialDriver) Observe(_ context.Context, _ domain.SandboxID) (driver.Observation, error) {
	return driver.Observation{State: driver.Absent}, nil
}

func (d *noDialDriver) Start(_ context.Context, _ driver.StartRequest) (string, error) {
	return "fake-instance-id", nil
}

func (d *noDialDriver) Stop(_ context.Context, _ domain.SandboxID) error {
	return nil
}

// noDialDriverFactory returns a DriverFactory that always returns a noDialDriver.
func noDialDriverFactory() DriverFactory {
	return func(_ string, _ []ExtraDisk) (driver.Driver, error) {
		return &noDialDriver{}, nil
	}
}

// TestRunEphemeral_ZeroLeftoversOnFault verifies the cleanup invariant:
// after RunEphemeral returns an error (exec fault via missing GuestDialer),
// the ResourceIndex must contain no host resources owned by the sandbox ULID.
//
// Setup:
//   - storeRoot/            ← FileStore + stateRoot for ResourceIndex
//     └── disks/            ← created by CreateAndBoot when opts.DiskDir is set here
//   - socketDir/            ← empty temp dir (no sockets in fake driver path)
//
// Fault injection: noDialDriver does not implement driver.GuestDialer, so
// agentClientFor returns ErrNoSubstrate when svc.Exec is called. RunEphemeral
// defers Remove before calling Exec, so the sandbox is cleaned up even though
// Exec fails.
func TestRunEphemeral_ZeroLeftoversOnFault(t *testing.T) {
	ctx := context.Background()

	// ── Coordinated temp directories ─────────────────────────────────────────
	// storeRoot is the FileStore root AND the ResourceIndex StateRoot so that
	// <storeRoot>/disks/ is scanned by ResourceIndex.List().
	storeRoot := t.TempDir()
	diskDir := filepath.Join(storeRoot, "disks")
	socketDir := t.TempDir()

	// ── Image cache ───────────────────────────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	// ── Service ───────────────────────────────────────────────────────────────
	// Build Service manually so we control storeRoot (newTestSvc uses its own
	// t.TempDir() that we cannot observe). The service's base driver is also
	// noDialDriver so agentClientFor's type assertion fails deterministically.
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	nd := &noDialDriver{}
	svc := New(st, nd, lifecycle.New()).WithDiskDir(diskDir)

	// ── ResourceIndex ─────────────────────────────────────────────────────────
	idx := NewResourceIndex(IndexConfig{
		StateRoot: storeRoot,
		SocketDir: socketDir,
	})

	// ── Call RunEphemeral — expect exec fault (no guest dialer) ───────────────
	var errBuf bytes.Buffer
	exitCode, runErr := RunEphemeral(
		ctx,
		svc,
		cache,
		noDialDriverFactory(),
		noopProbe,
		"ephemeral", "fault-test",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir, // inside storeRoot so ResourceIndex can see disks/
		},
		[]string{"echo", "hello"},
		nil, // env
		"",  // cwd
		nil,        // stdin
		io.Discard, // stdout
		&errBuf,    // stderr
	)

	// RunEphemeral must return an error: noDialDriver does not implement
	// GuestDialer, so agentClientFor returns ErrNoSubstrate when svc.Exec runs.
	if runErr == nil {
		t.Fatalf("expected RunEphemeral to return an error (fault injection); exit=%d", exitCode)
	}

	// ── Zero-leftover assertion ───────────────────────────────────────────────
	resources, listErr := idx.List()
	if listErr != nil {
		t.Fatalf("ResourceIndex.List: %v", listErr)
	}

	// After fault + deferred Remove, no resource should carry a non-zero OwnerID.
	// (OwnerID is zero for shadow disks and unrecognised entries; a surviving
	// sandbox disk would have a non-zero OwnerID matching the removed sandbox.)
	for _, r := range resources {
		if r.OwnerID != (domain.SandboxID{}) {
			t.Errorf("leftover resource after RunEphemeral fault: kind=%s path=%s ownerID=%s",
				r.Kind, r.Path, r.OwnerID)
		}
	}
}

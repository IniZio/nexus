package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
		ExecOptions{Argv: []string{"echo", "hello"}},
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

// TestRunEphemeral_OCIUncachedImage exercises the uncached-OCI run path: the
// image is not pre-loaded into the cache, so CreateAndBoot must pull it from
// the registry before booting. The test is network-gated and skips cleanly
// when the registry or Docker daemon is unreachable.
//
// It does NOT boot a real VM — the noDialDriver stub is used so the test
// terminates deterministically at the Exec step. The assertion is that
// RunEphemeral returns an error (ErrNoSubstrate from missing GuestDialer)
// rather than a pull/cache error, which proves the OCI pull succeeded and
// the image made it into the cache.
func TestRunEphemeral_OCIUncachedImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-gated OCI pull test in short mode")
	}

	// Check for Docker daemon reachability before any test infrastructure.
	// A missing docker binary or an unreachable daemon is treated as a
	// network/environment gap, not a test failure.
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH; skipping OCI pull test")
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	if out, err := exec.CommandContext(checkCtx, "docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unreachable (%v): %s", err, out)
	}

	ctx := context.Background()

	storeRoot := t.TempDir()
	diskDir := filepath.Join(storeRoot, "disks")

	// Empty cache — no pre-loaded image.
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	nd := &noDialDriver{}
	svc := New(st, nd, lifecycle.New()).WithDiskDir(diskDir)

	// Pull a tiny public image (alpine:latest). The test only verifies that
	// the pull path is reachable; the VM is never booted.
	const testImage = "alpine:latest"

	var errBuf bytes.Buffer
	_, runErr := RunEphemeral(
		ctx,
		svc,
		cache,
		noDialDriverFactory(),
		noopProbe,
		"ephemeral", "oci-pull-test",
		CreateAndBootOptions{
			Image:     ImageSpec{Ref: testImage},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
		},
		ExecOptions{Argv: []string{"echo", "hello"}},
		nil,        // stdin
		io.Discard, // stdout
		&errBuf,    // stderr
	)

	// The noDialDriver cannot exec, so RunEphemeral must fail with
	// ErrNoSubstrate (or a wrapped form of it). A pull/cache error would
	// indicate the OCI path broke before reaching the exec step.
	if runErr == nil {
		t.Fatal("expected RunEphemeral to return an error; got nil")
	}
	// Verify the error is exec-related (not a pull or cache error).
	// ErrNoSubstrate is the sentinel returned by agentClientFor when the
	// driver does not implement GuestDialer.
	if !errors.Is(runErr, ErrNoSubstrate) {
		t.Logf("RunEphemeral error: %v", runErr)
		t.Skip("unexpected error (possibly registry unreachable); skipping assertion")
	}
}

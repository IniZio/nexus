package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestSvc builds a Service backed by a real FileStore in a temp dir and the
// provided driver.
func newTestSvc(t *testing.T, drv driver.Driver) *Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return New(st, drv, lifecycle.New())
}

// putFakeImage writes a small fake ext4 blob into the cache and returns the
// image metadata so tests can reference its digest/ref.
func putFakeImage(t *testing.T, ctx context.Context, cache *image.Cache) domain.Image {
	t.Helper()
	content := []byte("fake-ext4-rootfs")
	h := sha256.New()
	h.Write(content)
	dig := domain.MustDigest(fmt.Sprintf("sha256:%x", h.Sum(nil)))

	img := domain.Image{
		Digest: dig,
		Ref:    "test-base:20260807",
		Kind:   domain.KindBase,
	}
	if err := cache.Put(ctx, img, bytes.NewReader(content)); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return img
}

// noopProbe is a ProbeFunc that always succeeds (agent is reachable).
var noopProbe ProbeFunc = func(_ context.Context, _ driver.Driver, _ domain.SandboxID) error {
	return nil
}

// errProbe is a ProbeFunc that always fails (agent is unreachable).
func errProbe(err error) ProbeFunc {
	return func(_ context.Context, _ driver.Driver, _ domain.SandboxID) error {
		return err
	}
}

// fakeDriverFactory returns a DriverFactory that always returns the given driver.
func fakeDriverFactory(drv driver.Driver) DriverFactory {
	return func(_ string) (driver.Driver, error) {
		return drv, nil
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestCreateAndBoot_ResolvesDigestFromCache verifies that when ImageSpec.Digest
// is set, CreateAndBoot finds the ext4 artifact in the cache, calls Start, and
// records the sandbox as Running.
func TestCreateAndBoot_ResolvesDigestFromCache(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	if sb.Project != "proj" {
		t.Errorf("Project = %q, want proj", sb.Project)
	}
	if sb.Name != "box" {
		t.Errorf("Name = %q, want box", sb.Name)
	}
	if sb.Envelope.ImageDigest != string(img.Digest) {
		t.Errorf("Envelope.ImageDigest = %q, want %q", sb.Envelope.ImageDigest, img.Digest)
	}
	// Verify the record was persisted as Running.
	got, err := svc.store.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.State != domain.Running {
		t.Errorf("persisted State = %v, want Running", got.State)
	}
}

// TestCreateAndBoot_ResolvesRefFromCache verifies that when ImageSpec.Ref is
// set to a human-readable tag, CreateAndBoot scans the cache list and resolves
// the matching image.
func TestCreateAndBoot_ResolvesRefFromCache(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Ref: img.Ref},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	if sb.Envelope.ImageDigest != string(img.Digest) {
		t.Errorf("Envelope.ImageDigest = %q, want %q", sb.Envelope.ImageDigest, img.Digest)
	}
}

// TestCreateAndBoot_ResolvesRootfsPath verifies the --rootfs convenience path:
// the ext4 is used directly without any cache lookup.
func TestCreateAndBoot_ResolvesRootfsPath(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			CacheRoot: cacheRoot,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.State != domain.Running {
		t.Errorf("State = %v, want Running", sb.State)
	}
	// No image digest when using rootfs path directly.
	if sb.Envelope.ImageDigest != "" {
		t.Errorf("Envelope.ImageDigest = %q, want empty for --rootfs", sb.Envelope.ImageDigest)
	}
}

// TestCreateAndBoot_ProbeFailureCleansUp verifies that when the reachability
// probe fails, the VM is stopped, the record is deleted, and the error wraps
// ErrAgentUnreachable. No orphan record is left in the store.
func TestCreateAndBoot_ProbeFailureCleansUp(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	simErr := errors.New("vsock: connection refused")
	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), errProbe(simErr),
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
		},
	)
	if err == nil {
		t.Fatal("expected error from probe failure, got nil")
	}
	if !errors.Is(err, ErrAgentUnreachable) {
		t.Errorf("error does not wrap ErrAgentUnreachable: %v", err)
	}

	// Store must be empty — no orphan record.
	all, listErr := svc.List(ctx)
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(all) != 0 {
		t.Errorf("expected empty store after probe failure, got %d records", len(all))
	}
}

// TestCreateAndBoot_DriverStartFailureCleansUp verifies that when driver.Start
// fails, the record is deleted and the error propagates without wrapping
// ErrAgentUnreachable.
func TestCreateAndBoot_DriverStartFailureCleansUp(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	simErr := errors.New("VMM: out of memory")
	fd.SetStartError(simErr)
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
		},
	)
	if err == nil {
		t.Fatal("expected error from Start failure, got nil")
	}
	if errors.Is(err, ErrAgentUnreachable) {
		t.Error("error should not wrap ErrAgentUnreachable for a Start failure")
	}

	// Store must be empty — no orphan record.
	all, listErr := svc.List(ctx)
	if listErr != nil {
		t.Fatalf("svc.List: %v", listErr)
	}
	if len(all) != 0 {
		t.Errorf("expected empty store after Start failure, got %d records", len(all))
	}
}

// TestCreateAndBoot_NilSpecErrors verifies that calling CreateAndBoot with an
// empty ImageSpec returns a clear error and writes no record.
func TestCreateAndBoot_NilSpecErrors(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:     ImageSpec{}, // all empty
			CacheRoot: cacheRoot,
		},
	)
	if err == nil {
		t.Fatal("expected error for empty ImageSpec, got nil")
	}
}

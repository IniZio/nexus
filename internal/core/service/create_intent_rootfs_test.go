package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// intentCheckDriver wraps FakeDriver and records whether the create-intent
// file for this sandbox existed at the moment Start was called.  The intent
// file is written BEFORE Start, so its absence proves the needsNamedVols arm
// was bypassed.
type intentCheckDriver struct {
	*fake.FakeDriver
	diskDir string
	found   bool
}

func (d *intentCheckDriver) Start(_ context.Context, req driver.StartRequest) (string, error) {
	intentPath := filepath.Join(d.diskDir, req.SandboxID.String()+".create-intent.json")
	if _, err := os.Stat(intentPath); err == nil {
		d.found = true
	}
	return d.FakeDriver.Start(context.Background(), req)
}

// TestCreateAndBoot_RootfsNamedVol_IntentWritten verifies that CreateAndBoot
// writes the create-intent file before calling driver.Start when running in
// --rootfs + named-volume mode (needsNamedVols arm).
//
// Mutation proof: set needsNamedVols := false in create.go → intent file is
// never written → d.found == false → test fails.
func TestCreateAndBoot_RootfsNamedVol_IntentWritten(t *testing.T) {
	ctx := context.Background()
	diskDir := t.TempDir()

	// Build a real image cache (needed for the cache parameter; rootfs mode
	// does not resolve from it, but CreateAndBoot still requires a non-nil cache).
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	// Create a named volume store with one KindDir volume.  KindDir avoids the
	// mke2fs round-trip while still satisfying needsNamedVols (the guard only
	// cares that Volumes != nil && len(NamedVolumeMounts) > 0).
	vs := volumestore.New(t.TempDir())
	if _, err := vs.Create(ctx, "rootfs-vol", volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("vs.Create: %v", err)
	}

	// Wrap the fake driver so we can observe intent-file presence at Start time.
	fd := fake.New()
	drv := &intentCheckDriver{FakeDriver: fd, diskDir: diskDir}

	svc := newTestSvc(t, drv)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(drv), noopProbe,
		"proj", "box",
		CreateAndBootOptions{
			Image:   ImageSpec{RootfsPath: "/fake/rootfs.ext4"},
			DiskDir: diskDir,
			Volumes: vs,
			NamedVolumeMounts: []NamedVolumeMount{
				{Name: "rootfs-vol", GuestPath: "/data", Kind: volumestore.KindDir},
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	if !drv.found {
		t.Error("create-intent file was NOT present when driver.Start was called: needsNamedVols arm is broken")
	}
}

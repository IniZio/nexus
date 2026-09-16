package service_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/service"
)

func allocOf(t *testing.T, path string) int64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Blocks * 512
}

func allocOfTree(t *testing.T, dir string) int64 {
	t.Helper()
	total := allocOf(t, dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		total += allocOf(t, filepath.Join(dir, e.Name()))
	}
	return total
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

type diskFixture struct {
	stateDir              string
	cache                 *image.Cache
	store                 *fakeSandboxImageLister
	agentTag              string
	wantCacheReclaim      int64
	wantTemplateReclaim   int64
	wantDiskReclaim       int64
	orphanImageDigest     domain.Digest
	referencedImageDigest domain.Digest
}

func seedDiskFixture(t *testing.T) diskFixture {
	t.Helper()
	stateDir := t.TempDir()
	c, err := image.NewCache(filepath.Join(stateDir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	referenced := putImageRef(t, c, []byte("referenced builder image"), domain.KindBuilder, "builder:live")
	orphan := putImageRef(t, c, []byte("orphan builder image, somewhat longer content"), domain.KindBuilder, "builder:orphan")

	known := domain.NewSandboxID()
	st := &fakeSandboxImageLister{sandboxes: []domain.Sandbox{{
		ID:       known,
		Envelope: domain.Envelope{ImageDigest: referenced.String()},
	}}}

	currentTag := image.BuilderAgentTag([]byte("current agent"))
	staleTag := image.BuilderAgentTag([]byte("previous agent"))
	stalePath := filepath.Join(stateDir, "images", "nexus-builder-abc-agent"+staleTag+".ext4")
	writeFile(t, stalePath, 8192)
	writeFile(t, filepath.Join(stateDir, "images", "nexus-builder-abc-agent"+currentTag+".ext4"), 4096)

	orphanDisk := filepath.Join(stateDir, "disks", domain.NewSandboxID().String()+".ext4")
	writeFile(t, orphanDisk, 12288)
	writeFile(t, filepath.Join(stateDir, "disks", known.String()+".ext4"), 4096)
	writeFile(t, filepath.Join(stateDir, "disks", "handle.shadow.pnpm.ext4"), 4096)
	writeFile(t, filepath.Join(stateDir, "caches", "buildkit.ext4"), 4096)
	writeFile(t, filepath.Join(stateDir, "volumes", "pnpm", "data.ext4"), 4096)
	writeFile(t, filepath.Join(stateDir, "supervisors", "sb-1.log"), 100)
	writeFile(t, filepath.Join(stateDir, "sandboxes", known.String()+".json"), 50)

	return diskFixture{
		stateDir:              stateDir,
		cache:                 c,
		store:                 st,
		agentTag:              currentTag,
		wantCacheReclaim:      allocOfTree(t, filepath.Join(stateDir, "images", "sha256", orphan.Hex())),
		wantTemplateReclaim:   allocOf(t, stalePath),
		wantDiskReclaim:       allocOf(t, orphanDisk),
		orphanImageDigest:     orphan,
		referencedImageDigest: referenced,
	}
}

func categoryByName(t *testing.T, rep service.DiskUsageReport, name string) service.DiskCategory {
	t.Helper()
	for _, c := range rep.Categories {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("category %q missing from %+v", name, rep.Categories)
	return service.DiskCategory{}
}

func TestDiskUsage_ClassifiesReferencedVsReclaimable(t *testing.T) {
	fx := seedDiskFixture(t)
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 100 << 30, nil }))

	rep, err := service.DiskUsage(context.Background(), fx.stateDir, fx.cache, fx.store, fx.agentTag)
	if err != nil {
		t.Fatalf("service.DiskUsage: %v", err)
	}

	cache := categoryByName(t, rep, service.DiskCategoryImageCache)
	if cache.Count != 2 || cache.Bytes <= 0 {
		t.Errorf("image cache: count=%d bytes=%d, want 2 entries with bytes>0", cache.Count, cache.Bytes)
	}
	if cache.Reclaimable != fx.wantCacheReclaim {
		t.Errorf("image cache reclaimable=%d, want %d (orphan entry only)", cache.Reclaimable, fx.wantCacheReclaim)
	}
	if !strings.Contains(cache.Note, "nexus3 image prune") {
		t.Errorf("image cache note=%q, want image prune hint", cache.Note)
	}

	tpl := categoryByName(t, rep, service.DiskCategoryBuilderTemplates)
	if tpl.Count != 2 || tpl.Bytes <= 0 {
		t.Errorf("templates: count=%d bytes=%d, want 2 with bytes>0", tpl.Count, tpl.Bytes)
	}
	if tpl.Reclaimable != fx.wantTemplateReclaim {
		t.Errorf("templates reclaimable=%d, want %d (stale template only)", tpl.Reclaimable, fx.wantTemplateReclaim)
	}
	if !strings.Contains(tpl.Note, "1 stale template") {
		t.Errorf("templates note=%q, want stale count", tpl.Note)
	}

	disks := categoryByName(t, rep, service.DiskCategorySandboxDisks)
	if disks.Count != 3 || disks.Bytes <= 0 {
		t.Errorf("disks: count=%d bytes=%d, want 3 with bytes>0", disks.Count, disks.Bytes)
	}
	if disks.Reclaimable != fx.wantDiskReclaim {
		t.Errorf("disks reclaimable=%d, want %d (orphan disk only; shadow disk kept)", disks.Reclaimable, fx.wantDiskReclaim)
	}
	if !strings.Contains(disks.Note, "nexus3 reap") {
		t.Errorf("disks note=%q, want reap hint", disks.Note)
	}

	caches := categoryByName(t, rep, service.DiskCategoryBuildCaches)
	if caches.Count != 1 || caches.Bytes <= 0 || caches.Reclaimable != 0 {
		t.Errorf("build caches: %+v, want count 1, bytes>0, reclaimable 0", caches)
	}
	vols := categoryByName(t, rep, service.DiskCategoryNamedVolumes)
	if vols.Count != 1 || vols.Bytes <= 0 {
		t.Errorf("volumes: %+v, want count 1, bytes>0", vols)
	}
	if snaps := categoryByName(t, rep, service.DiskCategorySnapshots); snaps.Count != 0 || snaps.Bytes != 0 {
		t.Errorf("snapshots (missing dir): %+v, want zero", snaps)
	}
	if sup := categoryByName(t, rep, service.DiskCategorySupervisorLogs); sup.Count != 1 || sup.Bytes <= 0 {
		t.Errorf("supervisor logs: %+v, want count 1, bytes>0", sup)
	}
	if other := categoryByName(t, rep, service.DiskCategoryOther); other.Bytes <= 0 {
		t.Errorf("other: %+v, want bytes>0 (sandboxes/ record + images/ leftovers)", other)
	}

	var sum, reclaim int64
	for _, c := range rep.Categories {
		sum += c.Bytes
		reclaim += c.Reclaimable
		if c.ApparentBytes < 0 {
			t.Errorf("%s apparent bytes negative", c.Name)
		}
	}
	if rep.TotalBytes != sum || sum <= 0 {
		t.Errorf("TotalBytes=%d, want sum of categories %d", rep.TotalBytes, sum)
	}
	if rep.Reclaimable != reclaim || reclaim != fx.wantCacheReclaim+fx.wantTemplateReclaim+fx.wantDiskReclaim {
		t.Errorf("Reclaimable=%d, want %d", rep.Reclaimable, fx.wantCacheReclaim+fx.wantTemplateReclaim+fx.wantDiskReclaim)
	}
	if rep.FloorBytes != uint64(service.DefaultGCFreeSpaceFloorGiB)<<30 || rep.FreeBytes != 100<<30 || rep.BelowFloor {
		t.Errorf("free/floor: free=%d floor=%d below=%v", rep.FreeBytes, rep.FloorBytes, rep.BelowFloor)
	}
	if len(rep.Hints) != 2 || rep.Hints[0] != "nexus3 image prune" || rep.Hints[1] != "nexus3 reap" {
		t.Errorf("hints=%v, want [nexus3 image prune, nexus3 reap]", rep.Hints)
	}
}

func TestDiskUsage_BelowFloor(t *testing.T) {
	fx := seedDiskFixture(t)
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 1 << 30, nil }))

	rep, err := service.DiskUsage(context.Background(), fx.stateDir, fx.cache, fx.store, fx.agentTag)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.BelowFloor {
		t.Errorf("BelowFloor=false with free=1GiB floor=%d", rep.FloorBytes)
	}
}

func TestDiskUsage_NilStoreAndNoAgentTag(t *testing.T) {
	fx := seedDiskFixture(t)
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 100 << 30, nil }))

	rep, err := service.DiskUsage(context.Background(), fx.stateDir, fx.cache, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{service.DiskCategoryImageCache, service.DiskCategoryBuilderTemplates, service.DiskCategorySandboxDisks} {
		c := categoryByName(t, rep, name)
		if c.Reclaimable != 0 {
			t.Errorf("%s reclaimable=%d with nil store / empty tag, want 0", name, c.Reclaimable)
		}
		if c.Bytes <= 0 || c.Note == "" {
			t.Errorf("%s: bytes=%d note=%q, want bytes>0 and an explanatory note", name, c.Bytes, c.Note)
		}
	}
	if rep.Reclaimable != 0 || len(rep.Hints) != 0 {
		t.Errorf("reclaimable=%d hints=%v, want 0 and none", rep.Reclaimable, rep.Hints)
	}
}

func TestDiskUsage_EmptyStateDir(t *testing.T) {
	stateDir := t.TempDir()
	t.Cleanup(service.SetFreeSpaceFuncForTest(func(string) (uint64, error) { return 100 << 30, nil }))
	c, err := image.NewCache(filepath.Join(stateDir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := service.DiskUsage(context.Background(), stateDir, c, &fakeSandboxImageLister{}, "tag")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Categories) != 8 {
		t.Errorf("got %d categories, want 8", len(rep.Categories))
	}
	for _, cat := range rep.Categories {
		if cat.Name == service.DiskCategoryOther {
			continue
		}
		if cat.Count != 0 || cat.Reclaimable != 0 {
			t.Errorf("%s on empty state dir: %+v", cat.Name, cat)
		}
	}
}

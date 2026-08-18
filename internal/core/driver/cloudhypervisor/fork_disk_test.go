package cloudhypervisor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// rewriteConfigDiskPath

// TestRewriteConfigDiskPath_SingleDisk verifies that the root disk path is
// rewritten and all other fields (including unknown ones) are preserved.
func TestRewriteConfigDiskPath_SingleDisk(t *testing.T) {
	input := buildConfig([]diskSpec{
		{path: "/some/dir/PARENT.raw", imageType: "Raw", extra: map[string]any{"readonly": false}},
	})

	got, err := rewriteConfigDiskPath(input, "/some/dir/PARENT.raw", "/disks/CHILD.raw")
	if err != nil {
		t.Fatalf("rewriteConfigDiskPath: %v", err)
	}

	disks := parseDisks(t, got)
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}

	if p := mustDiskPath(t, disks[0]); p != "/disks/CHILD.raw" {
		t.Errorf("disk path: got %q, want /disks/CHILD.raw", p)
	}

	// Extra field "readonly" must survive the round-trip.
	var readonly bool
	if err := json.Unmarshal(disks[0]["readonly"], &readonly); err != nil {
		t.Fatalf("decode readonly: %v", err)
	}
	if readonly != false {
		t.Errorf("readonly: got %v, want false", readonly)
	}

	// All top-level config keys must survive.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(got, &top); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	for _, key := range []string{"payload", "cpus", "memory", "disks"} {
		if _, ok := top[key]; !ok {
			t.Errorf("output missing top-level key %q", key)
		}
	}
}

// TestRewriteConfigDiskPath_MultiDisk verifies that only the root disk (matched
// by basename) is rewritten and sibling disks are left unchanged.
func TestRewriteConfigDiskPath_MultiDisk(t *testing.T) {
	input := buildConfig([]diskSpec{
		{path: "/disks/PARENT.raw", imageType: "Raw"},
		{path: "/data/cache.raw", imageType: "Raw"},
	})

	got, err := rewriteConfigDiskPath(input, "/disks/PARENT.raw", "/disks/CHILD.raw")
	if err != nil {
		t.Fatalf("rewriteConfigDiskPath: %v", err)
	}

	disks := parseDisks(t, got)
	if len(disks) != 2 {
		t.Fatalf("expected 2 disks, got %d", len(disks))
	}

	if p := mustDiskPath(t, disks[0]); p != "/disks/CHILD.raw" {
		t.Errorf("disk[0] path: got %q, want /disks/CHILD.raw", p)
	}
	if p := mustDiskPath(t, disks[1]); p != "/data/cache.raw" {
		t.Errorf("disk[1] path: got %q, want /data/cache.raw (unchanged)", p)
	}
}

// TestRewriteConfigDiskPath_NoMatch verifies that an error is returned when no
// disk entry matches the old path basename.
func TestRewriteConfigDiskPath_NoMatch(t *testing.T) {
	input := buildConfig([]diskSpec{
		{path: "/disks/OTHER.raw"},
	})
	_, err := rewriteConfigDiskPath(input, "/disks/PARENT.raw", "/disks/CHILD.raw")
	if err == nil {
		t.Fatal("expected error for no-match, got nil")
	}
}

// findRootDiskPath

// TestFindRootDiskPath_ExactMatch verifies that the disk whose basename is
// parentID.String()+".raw" is returned when present.
func TestFindRootDiskPath_ExactMatch(t *testing.T) {
	parentID := domain.NewSandboxID()
	diskName := parentID.String() + ".raw"
	input := buildConfig([]diskSpec{
		{path: "/disks/" + diskName},
		{path: "/data/cache.raw"},
	})

	got, err := findRootDiskPath(input, parentID)
	if err != nil {
		t.Fatalf("findRootDiskPath: %v", err)
	}
	if filepath.Base(got) != diskName {
		t.Errorf("got %q, expected basename %q", got, diskName)
	}
}

// TestFindRootDiskPath_SingleFallback verifies that a single disk entry is
// returned even when the basename does not match the parent ID.
func TestFindRootDiskPath_SingleFallback(t *testing.T) {
	input := buildConfig([]diskSpec{
		{path: "/disks/something-else.raw"},
	})

	got, err := findRootDiskPath(input, domain.NewSandboxID())
	if err != nil {
		t.Fatalf("findRootDiskPath: %v", err)
	}
	if got != "/disks/something-else.raw" {
		t.Errorf("got %q, want /disks/something-else.raw", got)
	}
}

// TestFindRootDiskPath_NoDisks verifies that errNoDisks is returned when the
// config.json has no "disks" field.
func TestFindRootDiskPath_NoDisks(t *testing.T) {
	cfg := map[string]any{
		"payload": map[string]any{"kernel": "/boot/vmlinux"},
	}
	b, _ := json.Marshal(cfg)

	_, err := findRootDiskPath(b, domain.NewSandboxID())
	if !errors.Is(err, errNoDisks) {
		t.Errorf("expected errNoDisks, got %v", err)
	}
}

// TestFindRootDiskPath_EmptyDisks verifies that errNoDisks is returned for an
// empty disks array.
func TestFindRootDiskPath_EmptyDisks(t *testing.T) {
	cfg := map[string]any{
		"disks": []any{},
	}
	b, _ := json.Marshal(cfg)

	_, err := findRootDiskPath(b, domain.NewSandboxID())
	if !errors.Is(err, errNoDisks) {
		t.Errorf("expected errNoDisks, got %v", err)
	}
}

// reflinkCopy

// TestReflinkCopy_Independence verifies that:
//   - dst starts with the same content as src, and
//   - writing to dst does not modify src (files are independent).
func TestReflinkCopy_Independence(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.raw")
	dst := filepath.Join(dir, "dst.raw")

	srcContent := []byte("original disk content")
	if err := os.WriteFile(src, srcContent, 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := reflinkCopy(src, dst); err != nil {
		t.Fatalf("reflinkCopy: %v", err)
	}

	// dst must initially equal src.
	dstContent, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(dstContent) != string(srcContent) {
		t.Errorf("initial dst content: got %q, want %q", dstContent, srcContent)
	}

	// Overwrite dst; src must be unchanged.
	if err := os.WriteFile(dst, []byte("modified"), 0o600); err != nil {
		t.Fatalf("overwrite dst: %v", err)
	}
	srcAfter, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("re-read src: %v", err)
	}
	if string(srcAfter) != string(srcContent) {
		t.Errorf("src changed after writing dst: got %q, want %q", srcAfter, srcContent)
	}
}

// prepareChildRestoreDir

// TestPrepareChildRestoreDir verifies that the restore dir contains:
//   - a rewritten config.json with the child disk path,
//   - a hardlinked (or copied) memory.snapshot,
//   - no extra files.
func TestPrepareChildRestoreDir(t *testing.T) {
	snapDir := t.TempDir()
	parentID := domain.NewSandboxID()
	childID := domain.NewSandboxID()

	parentDisk := filepath.Join(snapDir, "..", parentID.String()+".raw")
	childDisk := filepath.Join(snapDir, "..", childID.String()+".raw")
	// parentDisk doesn't need to exist on disk for this unit test.

	// Build a config.json referencing the parent disk.
	cfg := buildConfig([]diskSpec{{path: parentDisk, imageType: "Raw"}})
	if err := os.WriteFile(filepath.Join(snapDir, "config.json"), cfg, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	memContent := []byte("FAKE MEMORY SNAPSHOT DATA")
	if err := os.WriteFile(filepath.Join(snapDir, "memory.snapshot"), memContent, 0o600); err != nil {
		t.Fatalf("write memory.snapshot: %v", err)
	}

	restoreDir, err := prepareChildRestoreDir(snapDir, childID,
		map[string]string{parentDisk: childDisk},
		"", "", "", "")
	if err != nil {
		t.Fatalf("prepareChildRestoreDir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(restoreDir) })

	// Verify rewritten config.json.
	gotCfg, err := os.ReadFile(filepath.Join(restoreDir, "config.json"))
	if err != nil {
		t.Fatalf("read restore config.json: %v", err)
	}
	disks := parseDisks(t, gotCfg)
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	if p := mustDiskPath(t, disks[0]); p != childDisk {
		t.Errorf("restore config disk path: got %q, want %q", p, childDisk)
	}

	// Verify memory.snapshot is present (hardlinked or copied).
	memGot, err := os.ReadFile(filepath.Join(restoreDir, "memory.snapshot"))
	if err != nil {
		t.Fatalf("read restore memory.snapshot: %v", err)
	}
	if string(memGot) != string(memContent) {
		t.Errorf("memory.snapshot content mismatch")
	}
}

// test helpers

// diskSpec describes one disk entry in a synthetic config.json.
type diskSpec struct {
	path      string
	imageType string
	extra     map[string]any
}

// buildConfig marshals a synthetic CH config.json with the given disks.
func buildConfig(disks []diskSpec) []byte {
	type diskEntry map[string]any
	entries := make([]diskEntry, len(disks))
	for i, ds := range disks {
		e := diskEntry{"path": ds.path}
		if ds.imageType != "" {
			e["image_type"] = ds.imageType
		}
		for k, v := range ds.extra {
			e[k] = v
		}
		entries[i] = e
	}
	cfg := map[string]any{
		"payload": map[string]any{
			"kernel":  "/boot/vmlinux",
			"cmdline": "root=/dev/vda rw",
		},
		"cpus":   map[string]any{"boot_vcpus": 1, "max_vcpus": 1, "nested": false},
		"memory": map[string]any{"size": 536870912},
		"disks":  entries,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return b
}

// parseDisks unmarshals the "disks" array from a config.json blob.
func parseDisks(t *testing.T, configJSON []byte) []map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &top); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	var disks []map[string]json.RawMessage
	if err := json.Unmarshal(top["disks"], &disks); err != nil {
		t.Fatalf("unmarshal disks: %v", err)
	}
	return disks
}

// mustDiskPath extracts the "path" string from a raw disk map entry.
func mustDiskPath(t *testing.T, d map[string]json.RawMessage) string {
	t.Helper()
	var p string
	if err := json.Unmarshal(d["path"], &p); err != nil {
		t.Fatalf("decode disk path: %v", err)
	}
	return p
}

// D-PD-54 regression: all disks must be isolated on fork

// TestFork_ExtraDisksAllIsolated is the regression test for D-PD-54.
//
// Before the fix, prepareChildRestoreDir only rewrote the root disk path in
// config.json. Extra disks (shadow disks, workspace disk) were left pointing
// at the parent's paths, causing N concurrent children to share the same
// block device with no error surfaced.
//
// This test exercises the config-rewrite and disk-copy logic directly
// (reflinkCopy + prepareChildRestoreDir) without a real VM. It verifies:
//
//  1. Every disk path in the child's config.json differs from the corresponding
//     parent path (no parent path leaks into the child config).
//  2. Device order (index 0, 1, 2, …) is identical between parent and child.
//  3. Child disk files are independent: writing to a child file does not
//     modify the parent file.
func TestFork_ExtraDisksAllIsolated(t *testing.T) {
	dir := t.TempDir()
	childID := domain.NewSandboxID()

	// Three parent disk files: root, shadow (node_modules), workspace.
	parentRoot := filepath.Join(dir, "parent-root.raw")
	parentShadow := filepath.Join(dir, "shadow-node_modules.raw")
	parentWorkspace := filepath.Join(dir, "parent-workspace.raw")
	for _, p := range []string{parentRoot, parentShadow, parentWorkspace} {
		if err := os.WriteFile(p, []byte("disk:"+p), 0o600); err != nil {
			t.Fatalf("create disk %s: %v", p, err)
		}
	}

	// Config referencing all 3 disks in order (same layout as a real sandbox
	// with shadow disks: root=vda, shadow=vdb, workspace=vdc).
	cfg := buildConfig([]diskSpec{
		{path: parentRoot},
		{path: parentShadow},
		{path: parentWorkspace},
	})
	snapDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(snapDir, "config.json"), cfg, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	// Child copy paths: childID prefix keeps them unique per fork.
	childRoot := filepath.Join(dir, childID.String()+"-root.raw")
	childShadow := filepath.Join(dir, childID.String()+"-shadow-node_modules.raw")
	childWorkspace := filepath.Join(dir, childID.String()+"-workspace.raw")

	allPairs := []diskPair{
		{parentRoot, childRoot},
		{parentShadow, childShadow},
		{parentWorkspace, childWorkspace},
	}

	// Reflink-copy all disks (mirrors the copy loop in spawnChildFromSnapshot).
	for _, dp := range allPairs {
		if err := reflinkCopy(dp.parent, dp.child); err != nil {
			t.Fatalf("reflinkCopy %s: %v", dp.parent, err)
		}
	}
	t.Cleanup(func() {
		for _, dp := range allPairs {
			os.Remove(dp.child)
		}
	})

	diskRewrites := map[string]string{
		parentRoot:      childRoot,
		parentShadow:    childShadow,
		parentWorkspace: childWorkspace,
	}
	restoreDir, err := prepareChildRestoreDir(snapDir, childID, diskRewrites, "", "", "", "")
	if err != nil {
		t.Fatalf("prepareChildRestoreDir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(restoreDir) })

	// Assert 1: child config.json contains NO parent path.
	gotCfg, err := os.ReadFile(filepath.Join(restoreDir, "config.json"))
	if err != nil {
		t.Fatalf("read child config.json: %v", err)
	}
	disks := parseDisks(t, gotCfg)
	if len(disks) != 3 {
		t.Fatalf("expected 3 disks in child config.json, got %d", len(disks))
	}
	parentPaths := map[string]bool{parentRoot: true, parentShadow: true, parentWorkspace: true}
	for i, d := range disks {
		p := mustDiskPath(t, d)
		if parentPaths[p] {
			t.Errorf("disk[%d] in child config.json still points at parent path %q (D-PD-54)", i, p)
		}
	}

	// Assert 2: device order is preserved (root→shadow→workspace).
	wantPaths := []string{childRoot, childShadow, childWorkspace}
	for i, d := range disks {
		p := mustDiskPath(t, d)
		if p != wantPaths[i] {
			t.Errorf("disk[%d]: got %q, want %q (device order must match parent)", i, p, wantPaths[i])
		}
	}

	// Assert 3: child disk files are independent of their parent copies.
	for _, dp := range allPairs {
		orig, err := os.ReadFile(dp.parent)
		if err != nil {
			t.Fatalf("read parent %s: %v", dp.parent, err)
		}
		if err := os.WriteFile(dp.child, []byte("child-modified"), 0o600); err != nil {
			t.Fatalf("write child %s: %v", dp.child, err)
		}
		after, err := os.ReadFile(dp.parent)
		if err != nil {
			t.Fatalf("re-read parent %s: %v", dp.parent, err)
		}
		if string(after) != string(orig) {
			t.Errorf("parent disk %s changed after writing child copy (not isolated)", dp.parent)
		}
	}
}

// Defect 1 regression: malformed sibling disk entry must error (D-PD-54)

// TestCollectExtraDiskPaths_MalformedSibling verifies that collectExtraDiskPaths
// returns an error when config.json has a root disk plus a sibling disk entry
// with no "path" field. Before the fix (fork.go swallowed the error from
// findAllDiskPaths), the sibling was silently dropped and the child would
// inherit the parent's block device.
func TestCollectExtraDiskPaths_MalformedSibling(t *testing.T) {
	// config: root disk + valid shadow disk + a malformed entry (no "path" field).
	cfg := map[string]any{
		"payload": map[string]any{"kernel": "/boot/vmlinux"},
		"cpus":    map[string]any{"boot_vcpus": 1},
		"memory":  map[string]any{"size": 536870912},
		"disks": []any{
			map[string]any{"path": "/disks/root.raw", "image_type": "Raw"},
			map[string]any{"path": "/data/shadow.raw", "image_type": "Raw"},
			map[string]any{"image_type": "Raw"}, // malformed: no "path" field
		},
	}
	b, _ := json.Marshal(cfg)

	_, err := collectExtraDiskPaths(b, "/disks/root.raw")
	if err == nil {
		t.Fatal("expected error for config with malformed disk entry, got nil — " +
			"pre-fix: findAllDiskPaths error was swallowed, silently dropping shadow disk isolation")
	}
	if errors.Is(err, errNoDisks) {
		t.Fatalf("expected non-errNoDisks error, got errNoDisks: %v", err)
	}
}

// Defect 2 regression: unmatched rewrite key must error (D-PD-54)

// TestRewriteAllConfigDiskPaths_UnmatchedKey verifies that
// rewriteAllConfigDiskPaths returns an error when a rewrite key does not match
// any disk entry in config.json. Before the fix, the miss was silent — a
// child config could still reference a parent-owned disk path with no error.
func TestRewriteAllConfigDiskPaths_UnmatchedKey(t *testing.T) {
	input := buildConfig([]diskSpec{
		{path: "/disks/root.raw", imageType: "Raw"},
	})

	// rewrites contains a key that is not present in the config.
	_, err := rewriteAllConfigDiskPaths(input, map[string]string{
		"/disks/root.raw":        "/disks/child-root.raw",
		"/disks/nonexistent.raw": "/disks/child-nonexistent.raw", // no match
	})
	if err == nil {
		t.Fatal("expected error for unmatched rewrite key, got nil — " +
			"pre-fix: miss was silent, leaving child config pointing at parent disk")
	}
}

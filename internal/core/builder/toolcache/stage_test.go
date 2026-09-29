package toolcache

import (
	"os"
	"path/filepath"
	"testing"
)

func makeFakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho "+name), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStageTreeLayout(t *testing.T) {
	tmp := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	tool := Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(tmp, []Fetched{tool}, false); err != nil {
		t.Fatalf("StageTree: %v", err)
	}

	destBin := filepath.Join(tmp, tool.GuestBinPath)
	fi, err := os.Lstat(destBin)
	if err != nil {
		t.Fatalf("binary not staged: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("binary is not regular file")
	}
	if fi.Mode().Perm() != 0755 {
		t.Fatalf("binary mode %v, want 0755", fi.Mode().Perm())
	}

	destLink := filepath.Join(tmp, tool.LinkPath)
	lfi, err := os.Lstat(destLink)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if lfi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link is not a symlink")
	}

	target, err := os.Readlink(destLink)
	if err != nil {
		t.Fatal(err)
	}
	wantRel := "../share/nexus-tools/gh/2.101.0/bin/gh"
	if target != wantRel {
		t.Fatalf("symlink target %q, want %q", target, wantRel)
	}

	resolved, err := os.Stat(destLink)
	if err != nil {
		t.Fatalf("stat through symlink: %v", err)
	}
	if !resolved.Mode().IsRegular() {
		t.Fatalf("stat through symlink: not a regular file")
	}
}

func TestStageTreeSkipIfPresent_UsrBin(t *testing.T) {
	tmp := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	if err := os.MkdirAll(filepath.Join(tmp, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "usr/bin/gh"), []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(tmp, []Fetched{tool}, true); err != nil {
		t.Fatalf("StageTree: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(tmp, tool.GuestBinPath)); err == nil {
		t.Fatal("should have skipped but binary was staged")
	}
}

func TestStageTreeSkipIfPresent_BinSymlinkRerooted(t *testing.T) {
	tmp := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	if err := os.MkdirAll(filepath.Join(tmp, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "usr/bin/gh"), []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin", filepath.Join(tmp, "bin")); err != nil {
		t.Fatal(err)
	}

	tool := Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(tmp, []Fetched{tool}, true); err != nil {
		t.Fatalf("StageTree: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(tmp, tool.GuestBinPath)); err == nil {
		t.Fatal("should have skipped (bin→/usr/bin/gh re-rooted) but binary was staged")
	}
}

func TestStageTreeSkipFalse_StagesAnyway(t *testing.T) {
	tmp := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	if err := os.MkdirAll(filepath.Join(tmp, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "usr/bin/gh"), []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(tmp, []Fetched{tool}, false); err != nil {
		t.Fatalf("StageTree: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(tmp, tool.GuestBinPath)); err != nil {
		t.Fatal("binary should be staged even though /usr/bin/gh present")
	}
}

func TestStageTreeUnsafeSymlinkInPath(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	if err := os.MkdirAll(filepath.Join(tmp, "usr"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tmp, "usr/local")); err != nil {
		t.Fatal(err)
	}

	tool := Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	err := StageTree(tmp, []Fetched{tool}, false)
	if err == nil {
		t.Fatal("expected error for symlink in write path, got nil")
	}

	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("outside dir was written to: %v", entries)
	}
}

func TestStageTreeRelativeGuestBinPath(t *testing.T) {
	tmp := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	tool := Fetched{
		Name:         "gh",
		BinPath:      binPath,
		GuestBinPath: "usr/local/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(tmp, []Fetched{tool}, false); err == nil {
		t.Fatal("expected error for relative GuestBinPath")
	}
}

func TestStageTreeDotDotGuestBinPath(t *testing.T) {
	tmp := t.TempDir()
	binDir := t.TempDir()
	binPath := makeFakeBin(t, binDir, "gh")

	tool := Fetched{
		Name:         "gh",
		BinPath:      binPath,
		GuestBinPath: "/usr/local/../bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(tmp, []Fetched{tool}, false); err == nil {
		t.Fatal("expected error for non-clean GuestBinPath")
	}
}

func TestStageTree_DestBinSymlinkToOutsideNotFollowed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// victim1: a file in outside that a symlink at destBin points to
	victim1 := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim1, []byte("victim"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-create the bin directory as real dirs inside root
	binGuestDir := "/usr/local/share/nexus-tools/gh/2.101.0/bin"
	if err := os.MkdirAll(filepath.Join(root, binGuestDir), 0755); err != nil {
		t.Fatal(err)
	}

	// Place a symlink at destBin pointing outside root
	destBin := filepath.Join(root, binGuestDir, "gh")
	if err := os.Symlink(victim1, destBin); err != nil {
		t.Fatal(err)
	}

	// victim2: for the LinkPath scenario
	victim2 := filepath.Join(outside, "victim2")
	if err := os.WriteFile(victim2, []byte("victim2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-create /usr/local/bin inside root and place symlink at destLink pointing outside root
	linkGuestDir := "/usr/local/bin"
	if err := os.MkdirAll(filepath.Join(root, linkGuestDir), 0755); err != nil {
		t.Fatal(err)
	}
	destLink := filepath.Join(root, linkGuestDir, "gh")
	if err := os.Symlink(victim2, destLink); err != nil {
		t.Fatal(err)
	}

	fakeBinDir := t.TempDir()
	fakeBin := filepath.Join(fakeBinDir, "gh")
	if err := os.WriteFile(fakeBin, []byte("new-gh"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := Fetched{
		Name:         "gh",
		Version:      "2.101.0",
		BinPath:      fakeBin,
		GuestBinPath: "/usr/local/share/nexus-tools/gh/2.101.0/bin/gh",
		LinkPath:     "/usr/local/bin/gh",
	}

	if err := StageTree(root, []Fetched{tool}, false); err != nil {
		t.Fatalf("StageTree: %v", err)
	}

	got, err := os.ReadFile(victim1)
	if err != nil {
		t.Fatalf("reading victim: %v", err)
	}
	if string(got) != "victim" {
		t.Fatalf("victim was clobbered: got %q, want %q", string(got), "victim")
	}

	fi, err := os.Lstat(destBin)
	if err != nil {
		t.Fatalf("Lstat destBin: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("destBin is not a regular file; mode=%v", fi.Mode())
	}
	if fi.Mode().Perm() != 0755 {
		t.Fatalf("destBin mode %v, want 0755", fi.Mode().Perm())
	}
	content, err := os.ReadFile(destBin)
	if err != nil {
		t.Fatalf("reading destBin: %v", err)
	}
	if string(content) != "new-gh" {
		t.Fatalf("destBin content %q, want %q", string(content), "new-gh")
	}

	got2, err := os.ReadFile(victim2)
	if err != nil {
		t.Fatalf("reading victim2: %v", err)
	}
	if string(got2) != "victim2" {
		t.Fatalf("victim2 was clobbered: got %q, want %q", string(got2), "victim2")
	}

	lfi, err := os.Lstat(destLink)
	if err != nil {
		t.Fatalf("Lstat destLink: %v", err)
	}
	if lfi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("destLink is not a symlink; mode=%v", lfi.Mode())
	}
	target, err := os.Readlink(destLink)
	if err != nil {
		t.Fatal(err)
	}
	wantRel := "../share/nexus-tools/gh/2.101.0/bin/gh"
	if target != wantRel {
		t.Fatalf("destLink target %q, want %q", target, wantRel)
	}
}

func TestStageTreeEmptyTools(t *testing.T) {
	tmp := t.TempDir()
	if err := StageTree(tmp, nil, false); err != nil {
		t.Fatalf("empty tools: %v", err)
	}
}

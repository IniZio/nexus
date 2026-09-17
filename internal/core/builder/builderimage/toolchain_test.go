//go:build linux

package builderimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const resize2fsApk = "e2fsprogs-extra-1.47.1-r1.apk"
const zramctlApk = "util-linux-misc-2.40.4-r1.apk"
const libsmartcolsApk = "libsmartcols-2.40.4-r1.apk"
const libmountApk = "libmount-2.40.4-r1.apk"

func TestToolchainPackages_IncludeResize2fs(t *testing.T) {
	idx := -1
	for i, p := range toolchainPackages {
		if p == resize2fsApk {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("toolchainPackages = %v; missing %s (ships usr/sbin/resize2fs, required by the guest disk.grow handler)", toolchainPackages, resize2fsApk)
	}
	for _, lib := range []string{"e2fsprogs-libs-", "libcom_err-"} {
		libIdx := -1
		for i, p := range toolchainPackages {
			if strings.HasPrefix(p, lib) {
				libIdx = i
			}
		}
		if libIdx < 0 || libIdx > idx {
			t.Errorf("%s must be listed before %s (index %d vs %d)", lib, resize2fsApk, libIdx, idx)
		}
	}
}

func TestToolchainPackages_IncludeZramctl(t *testing.T) {
	zIdx := -1
	lIdx := -1
	for i, p := range toolchainPackages {
		switch p {
		case zramctlApk:
			zIdx = i
		case libsmartcolsApk:
			lIdx = i
		}
	}
	if zIdx < 0 {
		t.Fatalf("toolchainPackages missing %s (ships sbin/zramctl used by nexus-agent swap setup)", zramctlApk)
	}
	if lIdx < 0 {
		t.Fatalf("toolchainPackages missing %s (libsmartcols.so.1 is ELF NEEDED by zramctl)", libsmartcolsApk)
	}
	if lIdx > zIdx {
		t.Errorf("%s (index %d) must precede %s (index %d): lib before binary", libsmartcolsApk, lIdx, zramctlApk, zIdx)
	}
}

func TestInjectToolchain_ZramctlAndDeps(t *testing.T) {
	if os.Getenv("NEXUS_HOST_TESTS") != "1" {
		t.Skip("set NEXUS_HOST_TESTS=1 to run (downloads Alpine packages)")
	}
	ctx := context.Background()

	pkgsWithout := make([]string, 0, len(toolchainPackages))
	for _, p := range toolchainPackages {
		if p != zramctlApk {
			pkgsWithout = append(pkgsWithout, p)
		}
	}

	stageA := t.TempDir()
	orig := toolchainPackages
	toolchainPackages = pkgsWithout
	err := injectToolchain(ctx, stageA)
	toolchainPackages = orig
	if err != nil {
		t.Fatalf("inject without zramctl: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stageA, "sbin/zramctl")); !os.IsNotExist(err) {
		t.Errorf("sbin/zramctl should be absent when %s is omitted; stat err=%v", zramctlApk, err)
	}
	t.Logf("PASS: sbin/zramctl absent without %s", zramctlApk)

	pkgsWithoutLibmount := make([]string, 0, len(toolchainPackages))
	for _, p := range toolchainPackages {
		if p != libmountApk {
			pkgsWithoutLibmount = append(pkgsWithoutLibmount, p)
		}
	}

	stageC := t.TempDir()
	toolchainPackages = pkgsWithoutLibmount
	err = injectToolchain(ctx, stageC)
	toolchainPackages = orig
	if err != nil {
		t.Fatalf("inject without libmount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stageC, "usr/lib/libmount.so.1")); !os.IsNotExist(err) {
		t.Errorf("usr/lib/libmount.so.1 should be absent when %s is omitted; stat err=%v", libmountApk, err)
	}
	t.Logf("PASS: usr/lib/libmount.so.1 absent without %s", libmountApk)

	stageB := t.TempDir()
	if err := injectToolchain(ctx, stageB); err != nil {
		t.Fatalf("inject full list: %v", err)
	}
	for _, rel := range []string{"sbin/zramctl", "usr/lib/libsmartcols.so.1", "usr/lib/libmount.so.1"} {
		if _, err := os.Stat(filepath.Join(stageB, rel)); err != nil {
			t.Errorf("%s missing after full inject: %v", rel, err)
		}
	}
	t.Logf("PASS: sbin/zramctl, usr/lib/libsmartcols.so.1, usr/lib/libmount.so.1 present after full inject")
}

func TestExtractAlpinePkg_OverwriteReadOnly(t *testing.T) {
	content := []byte("new\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{
		Name:     "bin/getopt",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o755,
	})
	_, _ = tw.Write(content)
	tw.Close()
	gz.Close()
	apkBytes := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(apkBytes)
	}))
	defer srv.Close()

	t.Run("fails_without_remove", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bin/getopt"), []byte("old\n"), 0o555); err != nil {
			t.Fatal(err)
		}
		_, err := os.OpenFile(filepath.Join(dir, "bin/getopt"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err == nil {
			t.Fatal("expected permission-denied opening a 0555 file for writing; got nil error")
		}
		if !os.IsPermission(err) {
			t.Fatalf("expected permission error; got %v", err)
		}
		t.Logf("CONFIRMED: openFile on 0555 without remove → %v", err)
	})

	t.Run("succeeds_with_remove", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bin/getopt"), []byte("old\n"), 0o555); err != nil {
			t.Fatal(err)
		}
		if err := extractAlpinePkg(context.Background(), srv.URL, dir); err != nil {
			t.Fatalf("extractAlpinePkg: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "bin/getopt"))
		if err != nil {
			t.Fatalf("read after extract: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("content = %q; want %q", got, content)
		}
		t.Logf("PASS: extractAlpinePkg overwrote 0555 file; content = %q", got)
	})
}

func TestBuilderImageCachePath_FoldsToolchainFingerprint(t *testing.T) {
	agent := []byte("fake-agent")
	want := "-tc" + toolchainFingerprint(toolchainPackages) + "-agent"
	got := filepath.Base(builderImageCachePath("/images", "sha256-abc", agent))
	if !strings.Contains(got, want) {
		t.Fatalf("cache filename %q lacks toolchain tag %q: a toolchain change would reuse a stale image", got, want)
	}
	if !regexp.MustCompile(`-agent[0-9a-f]{16}\.ext4$`).MatchString(got) {
		t.Fatalf("cache filename %q must end with -agent<16hex>.ext4 so image.parseBuilderTemplateName still parses it", got)
	}

	before := toolchainFingerprint(toolchainPackages)
	after := toolchainFingerprint(append(append([]string{}, toolchainPackages...), "extra-0.0.1-r0.apk"))
	if before == after {
		t.Fatalf("toolchainFingerprint unchanged (%s) after the package list changed", before)
	}
	if len(before) != 8 {
		t.Fatalf("toolchainFingerprint length = %d, want 8 hex chars", len(before))
	}
}

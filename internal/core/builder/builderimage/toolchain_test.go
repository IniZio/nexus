//go:build linux

package builderimage

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// resize2fsApk ships usr/sbin/resize2fs; the plain e2fsprogs apk ships only
// sbin/{mke2fs,e2fsck,mkfs.*} (verified by extracting both Alpine v3.21 apks).
const resize2fsApk = "e2fsprogs-extra-1.47.1-r1.apk"

func TestE2fsprogsPackages_IncludeResize2fs(t *testing.T) {
	idx := -1
	for i, p := range e2fsprogsPackages {
		if p == resize2fsApk {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("e2fsprogsPackages = %v; missing %s (ships usr/sbin/resize2fs, required by the guest disk.grow handler)", e2fsprogsPackages, resize2fsApk)
	}
	for _, lib := range []string{"e2fsprogs-libs-", "libcom_err-"} {
		libIdx := -1
		for i, p := range e2fsprogsPackages {
			if strings.HasPrefix(p, lib) {
				libIdx = i
			}
		}
		if libIdx < 0 || libIdx > idx {
			t.Errorf("%s must be listed before %s (index %d vs %d)", lib, resize2fsApk, libIdx, idx)
		}
	}
}

func TestBuilderImageCachePath_FoldsToolchainFingerprint(t *testing.T) {
	agent := []byte("fake-agent")
	want := "-tc" + toolchainFingerprint(e2fsprogsPackages) + "-agent"
	got := filepath.Base(builderImageCachePath("/images", "sha256-abc", agent))
	if !strings.Contains(got, want) {
		t.Fatalf("cache filename %q lacks toolchain tag %q: a toolchain change would reuse a stale image", got, want)
	}
	if !regexp.MustCompile(`-agent[0-9a-f]{16}\.ext4$`).MatchString(got) {
		t.Fatalf("cache filename %q must end with -agent<16hex>.ext4 so image.parseBuilderTemplateName still parses it", got)
	}

	before := toolchainFingerprint(e2fsprogsPackages)
	after := toolchainFingerprint(append(append([]string{}, e2fsprogsPackages...), "extra-0.0.1-r0.apk"))
	if before == after {
		t.Fatalf("toolchainFingerprint unchanged (%s) after the package list changed", before)
	}
	if len(before) != 8 {
		t.Fatalf("toolchainFingerprint length = %d, want 8 hex chars", len(before))
	}
}

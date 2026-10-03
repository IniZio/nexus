//go:build linux

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const fuseMagic = 0x65735546 // virtiofs reports FUSE_SUPER_MAGIC

func TestSnapshotterForFS(t *testing.T) {
	cases := []struct {
		name    string
		magic   int64
		overlay bool
		want    string
	}{
		{"ext4", unix.EXT4_SUPER_MAGIC, true, "overlayfs"},
		{"xfs", unix.XFS_SUPER_MAGIC, true, "overlayfs"},
		{"virtiofs", fuseMagic, true, "native"},
		{"overlay", unix.OVERLAYFS_SUPER_MAGIC, true, "native"},
		{"tmpfs", unix.TMPFS_MAGIC, true, "native"},
		{"ext4 no kernel overlay", unix.EXT4_SUPER_MAGIC, false, "native"},
	}
	for _, c := range cases {
		if got := snapshotterForFS(c.magic, c.overlay); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestSelectSnapshotter_Probe(t *testing.T) {
	fs := filepath.Join(t.TempDir(), "filesystems")
	if err := os.WriteFile(fs, []byte("nodev\tsysfs\nnodev\toverlay\n\text4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldFS, oldStat := procFilesystemsPath, statfsTypeFn
	defer func() { procFilesystemsPath, statfsTypeFn = oldFS, oldStat }()
	procFilesystemsPath = fs

	statfsTypeFn = func(string) (int64, error) { return unix.EXT4_SUPER_MAGIC, nil }
	if got := selectSnapshotter("/x"); got != "overlayfs" {
		t.Errorf("ext4: got %s", got)
	}
	statfsTypeFn = func(string) (int64, error) { return fuseMagic, nil }
	if got := selectSnapshotter("/x"); got != "native" {
		t.Errorf("virtiofs: got %s", got)
	}
	statfsTypeFn = func(string) (int64, error) { return 0, errors.New("boom") }
	if got := selectSnapshotter("/x"); got != "native" {
		t.Errorf("statfs error: got %s", got)
	}
}

func TestBuildkitdArgs_GCFlags(t *testing.T) {
	args := strings.Join(buildkitdArgs("/var/lib/buildkit", "/run/s.sock", "overlayfs", "/usr/bin/runc"), " ")
	for _, want := range []string{
		"--oci-worker-snapshotter=overlayfs",
		"--oci-worker-gc=true",
		"--oci-worker-gc-keepstorage=" + buildkitGCKeepStorage,
		"--root /var/lib/buildkit",
		"--oci-worker-binary=/usr/bin/runc",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("argv missing %q: %s", want, args)
		}
	}
}

// buildkitd ParseInts every comma field; an empty one crashes it at startup.
func TestBuildkitGCKeepStorage_AllFieldsNumeric(t *testing.T) {
	parts := strings.SplitN(buildkitGCKeepStorage, ",", 3)
	if len(parts) != 3 {
		t.Fatalf("want 3 fields, got %d: %q", len(parts), buildkitGCKeepStorage)
	}
	var v [3]int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			t.Fatalf("field %d %q: %v", i, p, err)
		}
		v[i] = n
	}
	if want := buildkitPruneReserved / 1e6; v[0] != want {
		t.Errorf("reserved = %d, want %d", v[0], want)
	}
	if want := buildkitPruneMax / 1e6; v[2] != want {
		t.Errorf("max = %d, want %d", v[2], want)
	}
}

func TestTrimBuildkitState_Invoked(t *testing.T) {
	old := trimPathFn
	defer func() { trimPathFn = old }()
	var got string
	trimPathFn = func(p string) (uint64, error) { got = p; return 42, nil }
	trimBuildkitState("/var/lib/buildkit")
	if got != "/var/lib/buildkit" {
		t.Errorf("trim not invoked on cache disk, got %q", got)
	}
	trimPathFn = func(string) (uint64, error) { return 0, errors.New("EOPNOTSUPP") }
	trimBuildkitState("/var/lib/buildkit") // must not panic
}

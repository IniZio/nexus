package fsutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const testSize = 16 << 20

func makeSparse(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	p := filepath.Join(dir, "src")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(testSize); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, testSize)
	for _, off := range []int{0, 4 << 20, testSize - 4096} {
		for i := 0; i < 4096; i++ {
			want[off+i] = byte(i%251 + 1)
		}
		if _, err := f.WriteAt(want[off:off+4096], int64(off)); err != nil {
			t.Fatal(err)
		}
	}
	return p, want
}

func blocks(t *testing.T, p string) int64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Blocks * 512
}

func TestFallbackPreservesHolesAndContent(t *testing.T) {
	dir := t.TempDir()
	src, want := makeSparse(t, dir)
	dst := filepath.Join(dir, "dst")
	noClone := func(_, _ *os.File) error { return unix.EOPNOTSUPP }
	if err := copyFile(src, dst, noClone); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("content differs")
	}
	if b := blocks(t, dst); b > testSize/4 {
		t.Fatalf("dst not sparse: %d allocated bytes", b)
	}
	fi, _ := os.Stat(dst)
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestFallbackSkipsZeroDataBlocks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := copyFile(src, dst, func(_, _ *os.File) error { return unix.EINVAL }); err != nil {
		t.Fatal(err)
	}
	if b := blocks(t, dst); b > 1<<20 {
		t.Fatalf("zero data not punched: %d", b)
	}
}

func TestCloneErrorNotFallenBack(t *testing.T) {
	dir := t.TempDir()
	src, _ := makeSparse(t, dir)
	dst := filepath.Join(dir, "dst")
	boom := errors.New("boom")
	if err := copyFile(src, dst, func(_, _ *os.File) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("partial dst left behind")
	}
}

func TestDstExistsFails(t *testing.T) {
	dir := t.TempDir()
	src, _ := makeSparse(t, dir)
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(dst, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileReflink(src, dst); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(dst); string(b) != "x" {
		t.Fatal("existing dst clobbered")
	}
}

func TestCopyFileReflinkContent(t *testing.T) {
	dir := t.TempDir()
	src, want := makeSparse(t, dir)
	dst := filepath.Join(dir, "dst")
	if err := CopyFileReflink(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, want) {
		t.Fatal("content differs")
	}
}

func TestReflinkPathWhenSupported(t *testing.T) {
	dir := t.TempDir()
	src, _ := makeSparse(t, dir)
	probe, err := os.OpenFile(filepath.Join(dir, "probe"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := os.Open(src)
	defer in.Close()
	defer probe.Close()
	if err := ficlone(probe, in); err != nil {
		t.Skipf("fs lacks reflink: %v", err)
	}
	called := false
	dst := filepath.Join(dir, "dst")
	if err := copyFile(src, dst, func(d, s *os.File) error { called = true; return ficlone(d, s) }); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

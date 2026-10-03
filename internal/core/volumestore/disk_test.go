package volumestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestMke2fsArgs(t *testing.T) {
	if got := mke2fsArgs("p", true); !reflect.DeepEqual(got, []string{"-t", "ext4", "-F", "-E", "assume_storage_prezeroed=1", "p"}) {
		t.Fatalf("prezeroed args: %v", got)
	}
	if got := mke2fsArgs("p", false); !reflect.DeepEqual(got, []string{"-t", "ext4", "-F", "-E", "lazy_itable_init=0,lazy_journal_init=0", "p"}) {
		t.Fatalf("fallback args: %v", got)
	}
}

func TestFormatExt4FallsBackWhenPrezeroedRejected(t *testing.T) {
	orig := runMke2fs
	t.Cleanup(func() { runMke2fs = orig })
	var opts []string
	runMke2fs = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		opts = append(opts, args[4])
		if len(opts) == 1 {
			return []byte("Unknown extended option"), errors.New("exit 1")
		}
		return nil, nil
	}
	if err := formatExt4(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	want := []string{"assume_storage_prezeroed=1", "lazy_itable_init=0,lazy_journal_init=0"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("opts=%v want %v", opts, want)
	}
}

func TestPreallocatedVolumeStaysSparse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.img")
	if err := preallocateFile(p, 2<<30); err != nil {
		t.Fatal(err)
	}
	if err := formatExt4(context.Background(), p); err != nil {
		t.Skipf("mke2fs unavailable: %v", err)
	}
	fi, _ := os.Stat(p)
	if st := fi.Sys(); st != nil {
		if used := diskUsage(fi); used > 16<<20 {
			t.Fatalf("empty 2GiB volume uses %d bytes", used)
		}
	}
}

func diskUsage(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return 0
}

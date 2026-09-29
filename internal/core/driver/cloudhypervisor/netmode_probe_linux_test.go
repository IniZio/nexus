//go:build linux

package cloudhypervisor

import (
	"errors"
	"syscall"
	"testing"
)

func TestProbeTapCached(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	eperm := func() error { calls++; return tapPermissionHint(syscall.EPERM) }
	if err := probeTapCached(dir, "boot-a", eperm); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("want EPERM, got %v", err)
	}
	if err := probeTapCached(dir, "boot-a", eperm); !errors.Is(err, syscall.EPERM) || calls != 1 {
		t.Fatalf("cache hit must not re-probe: err=%v calls=%d", err, calls)
	}
	ok := func() error { calls++; return nil }
	if err := probeTapCached(dir, "boot-b", ok); err != nil || calls != 2 {
		t.Fatalf("new boot must re-probe: err=%v calls=%d", err, calls)
	}
	if err := probeTapCached(dir, "boot-b", eperm); err != nil || calls != 2 {
		t.Fatalf("cached ok must be reused: err=%v calls=%d", err, calls)
	}
}

func TestProbeTapCached_OtherErrorNotCached(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	boom := func() error { calls++; return errors.New("boom") }
	_ = probeTapCached(dir, "boot-a", boom)
	_ = probeTapCached(dir, "boot-a", boom)
	if calls != 2 {
		t.Fatalf("non-EPERM failures must not be cached, calls=%d", calls)
	}
}

func TestProbeTapInChild_RefusesRecursionAndTestBinary(t *testing.T) {
	if err := probeTapInChild(); err == nil {
		t.Fatal("test binary must refuse to be re-exec'd as probe child")
	}
	t.Setenv(netnsProbeEnv, "1")
	if err := probeTapInChild(); err == nil {
		t.Fatal("probe env set must refuse")
	}
}

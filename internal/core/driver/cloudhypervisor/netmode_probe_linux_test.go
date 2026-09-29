//go:build linux

package cloudhypervisor

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProbeTapCached(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	eperm := func() error { calls++; return tapPermissionHint(syscall.EPERM) }
	if err := probeTapCached(dir, "boot-a", "1", eperm); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("want EPERM, got %v", err)
	}
	if err := probeTapCached(dir, "boot-a", "1", eperm); !errors.Is(err, syscall.EPERM) || calls != 1 {
		t.Fatalf("cache hit must not re-probe: err=%v calls=%d", err, calls)
	}
	ok := func() error { calls++; return nil }
	if err := probeTapCached(dir, "boot-b", "1", ok); err != nil || calls != 2 {
		t.Fatalf("new boot must re-probe: err=%v calls=%d", err, calls)
	}
	if err := probeTapCached(dir, "boot-b", "1", eperm); err != nil || calls != 2 {
		t.Fatalf("cached ok must be reused: err=%v calls=%d", err, calls)
	}
}

func TestProbeTapCached_OtherErrorNotCached(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	boom := func() error { calls++; return errors.New("boom") }
	_ = probeTapCached(dir, "boot-a", "1", boom)
	_ = probeTapCached(dir, "boot-a", "1", boom)
	if calls != 2 {
		t.Fatalf("non-EPERM failures must not be cached, calls=%d", calls)
	}
}

func TestProbeTapCached_UsernsChangeReprobes(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	ok := func() error { calls++; return nil }
	_ = probeTapCached(dir, "boot-a", "1", ok)
	_ = probeTapCached(dir, "boot-a", "1", ok)
	if calls != 1 {
		t.Fatalf("same boot and sysctl must hit cache, calls=%d", calls)
	}
	_ = probeTapCached(dir, "boot-a", "0", ok)
	if calls != 2 {
		t.Fatalf("changed sysctl must re-probe, calls=%d", calls)
	}
}

func TestProbeTapCached_OldFormatReprobes(t *testing.T) {
	dir := t.TempDir()
	old := `{"boot_id":"boot-a","blocked":true}`
	if err := os.WriteFile(filepath.Join(dir, netModeProbeFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	ok := func() error { calls++; return nil }
	if err := probeTapCached(dir, "boot-a", "absent", ok); err != nil || calls != 1 {
		t.Fatalf("old-format cache must miss: err=%v calls=%d", err, calls)
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

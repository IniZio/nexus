//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const trimMountinfoFixture = `22 1 254:16 / / rw,relatime shared:1 - ext4 /dev/vdb rw
23 22 254:32 / /data rw,relatime - xfs /dev/vdc rw,attr2
24 22 254:16 /sub /bind rw,relatime - ext4 /dev/vdb rw
25 22 0:30 / /mnt/host rw - virtiofs share rw
26 22 0:31 / /tmp rw - tmpfs tmpfs rw
27 22 0:32 / /ov rw - overlay overlay rw,lowerdir=/a
28 22 254:48 / /ro ro,relatime - ext4 /dev/vdd ro
29 22 254:64 / /sb ro,relatime - ext4 /dev/vde rw
30 22 8:1 / /sda rw - ext4 /dev/sda1 rw
31 22 254:80 / /with\040space rw - ext4 /dev/vdf rw
`

func TestParseTrimMounts(t *testing.T) {
	got := parseTrimMounts(strings.NewReader(trimMountinfoFixture))
	var paths []string
	for _, m := range got {
		paths = append(paths, m.Path)
	}
	want := []string{"/", "/data", "/with space"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", paths, want)
	}
}

func setupTrimTest(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(p, []byte(trimMountinfoFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	oldInfo, oldFn := trimMountinfo, trimFunc
	trimMountinfo = p
	t.Cleanup(func() { trimMountinfo, trimFunc = oldInfo, oldFn })
}

func TestTrimAllOnceSerialAndTotals(t *testing.T) {
	setupTrimTest(t)
	var calls []string
	trimFunc = func(p string) (uint64, error) {
		calls = append(calls, p)
		return 100, nil
	}
	if n := trimAllOnce(); n != 300 {
		t.Fatalf("total=%d", n)
	}
	if len(calls) != 3 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestTrimErrorRateLimited(t *testing.T) {
	setupTrimTest(t)
	trimErrMu.Lock()
	trimErrLast = map[string]time.Time{}
	trimErrMu.Unlock()
	now := time.Unix(1000, 0)
	oldNow := trimNow
	trimNow = func() time.Time { return now }
	t.Cleanup(func() { trimNow = oldNow })
	trimFunc = func(string) (uint64, error) { return 0, errors.New("boom") }

	trimAllOnce()
	trimErrMu.Lock()
	first := trimErrLast["/"]
	trimErrMu.Unlock()
	now = now.Add(time.Minute)
	trimAllOnce()
	trimErrMu.Lock()
	second := trimErrLast["/"]
	trimErrMu.Unlock()
	if !first.Equal(second) {
		t.Fatalf("error within window should be suppressed")
	}
	now = now.Add(2 * time.Hour)
	trimAllOnce()
	trimErrMu.Lock()
	third := trimErrLast["/"]
	trimErrMu.Unlock()
	if !third.After(second) {
		t.Fatalf("error after window should log again")
	}
}

func TestStartFSTrimmerSchedules(t *testing.T) {
	setupTrimTest(t)
	oldD, oldI := trimInitialDelay, trimInterval
	trimInitialDelay, trimInterval = 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { trimInitialDelay, trimInterval = oldD, oldI })
	var n atomic.Int32
	trimFunc = func(string) (uint64, error) { n.Add(1); return 0, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := startFSTrimmer(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if n.Load() < 6 {
		t.Fatalf("expected >=2 passes (6 calls), got %d", n.Load())
	}
}

//go:build linux

package main

// fstrim_linux.go — periodic FITRIM of guest block-backed filesystems.
//
// Guest deletions never reach the host unless the guest tells the block layer
// which extents are free. Discard is plumbed through virtio-blk and the host
// disk files are sparse, but ext4 is not mounted with `discard` and PID 1 is
// nexus-agent, so the image's fstrim.timer never runs. This trimmer fills that
// gap: shortly after boot and then periodically it issues FITRIM on every rw
// ext4/xfs mount backed by a virtio disk, one mount at a time, at idle I/O
// priority.

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Package vars so tests can shrink them.
var (
	trimInitialDelay = 2 * time.Minute
	trimInterval     = 10 * time.Minute
	trimErrLogEvery  = time.Hour
	trimMountinfo    = "/proc/self/mountinfo"

	// trimFunc issues FITRIM on a mountpoint and returns bytes trimmed.
	trimFunc = fitrimMount
	// trimNow is the clock used for error rate limiting.
	trimNow = time.Now
)

// trimMu serializes passes (periodic and one-shot).
var trimMu sync.Mutex

// ioctl FITRIM = _IOWR('X', 121, struct fstrim_range); same on amd64/arm64.
const fitrimIoctl = 0xC0185879

type fstrimRange struct {
	Start  uint64
	Len    uint64
	Minlen uint64
}

type trimMount struct {
	Dev    string // source, e.g. /dev/vdb
	Path   string
	FSType string
}

// parseTrimMounts returns the mounts eligible for FITRIM from mountinfo
// content: rw ext4/xfs on /dev/vd*, one per backing device (bind mounts of the
// same major:minor are collapsed, keeping the first).
func parseTrimMounts(r io.Reader) []trimMount {
	var out []trimMount
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i, v := range f {
			if v == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(f) < sep+4 {
			continue
		}
		majmin, mnt, opts := f[2], unescapeMountinfo(f[4]), f[5]
		fstype, src, super := f[sep+1], f[sep+2], f[sep+3]
		if fstype != "ext4" && fstype != "xfs" {
			continue
		}
		if !strings.HasPrefix(src, "/dev/vd") {
			continue
		}
		if !hasOpt(opts, "rw") || !hasOpt(super, "rw") {
			continue
		}
		if seen[majmin] {
			continue
		}
		seen[majmin] = true
		out = append(out, trimMount{Dev: src, Path: mnt, FSType: fstype})
	}
	return out
}

func hasOpt(csv, want string) bool {
	for _, o := range strings.Split(csv, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// unescapeMountinfo decodes the \040-style octal escapes in mountinfo paths.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			v := 0
			ok := true
			for _, c := range []byte(s[i+1 : i+4]) {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// fitrimMount issues FITRIM over the whole filesystem at path.
func fitrimMount(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	rng := fstrimRange{Len: ^uint64(0)}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), fitrimIoctl, uintptr(unsafe.Pointer(&rng)))
	if errno != 0 {
		return 0, errno
	}
	return rng.Len, nil // kernel writes back bytes trimmed
}

// setIdleIOPrio puts the calling OS thread in the idle I/O class.
func setIdleIOPrio() {
	const (
		ioprioWhoProcess = 1
		ioprioClassIdle  = 3
		ioprioShift      = 13
	)
	_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<ioprioShift)
}

var (
	trimErrMu   sync.Mutex
	trimErrLast = map[string]time.Time{}
)

func trimWarn(path string, err error) {
	trimErrMu.Lock()
	last, ok := trimErrLast[path]
	now := trimNow()
	if ok && now.Sub(last) < trimErrLogEvery {
		trimErrMu.Unlock()
		return
	}
	trimErrLast[path] = now
	trimErrMu.Unlock()
	slog.Warn("fstrim failed", "mount", path, "err", err)
}

// trimAllOnce runs one pass over all eligible mounts, serially, and returns
// total bytes trimmed. The pass runs on a dedicated OS thread at idle I/O
// priority; the thread is discarded afterward (never unlocked).
func trimAllOnce() uint64 {
	trimMu.Lock()
	defer trimMu.Unlock()
	var total uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread() // intentionally not unlocked: thread dies with goroutine
		setIdleIOPrio()
		f, err := os.Open(trimMountinfo)
		if err != nil {
			slog.Warn("fstrim: read mountinfo", "err", err)
			return
		}
		mounts := parseTrimMounts(f)
		f.Close()
		for _, m := range mounts {
			n, err := trimFunc(m.Path)
			if err != nil {
				trimWarn(m.Path, err)
				continue
			}
			total += n
			slog.Debug("fstrim", "mount", m.Path, "dev", m.Dev, "bytes", n)
		}
	}()
	<-done
	return total
}

// startFSTrimmer runs the periodic trimmer until ctx is done; the returned
// channel closes when it has exited.
func startFSTrimmer(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTimer(trimInitialDelay)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			trimAllOnce()
			t.Reset(trimInterval)
		}
	}()
	return done
}

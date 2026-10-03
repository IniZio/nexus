//go:build linux

package agent

import (
	"context"
	"log"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/moby/buildkit/client"
	"golang.org/x/sys/unix"
)

const (
	snapshotterNative    = "native"
	snapshotterOverlayfs = "overlayfs"

	// buildkitGCKeepStorage is buildkitd's "Reserved,MinFree,MaxUsed" (MB; all
	// fields must be numeric, 0 MinFree = no free-space threshold):
	// keep up to 10 GB of cache untouched, prune anything above a 20 GB max.
	// The builder cache disk is capped at 40 GiB (builder.BuildkitCacheDiskMaxGiB),
	// leaving ~20 GB of headroom for one in-flight cold build.
	buildkitGCKeepStorage = "10000,0,20000"
	// Same policy, in bytes, for the explicit post-build prune.
	buildkitPruneReserved = int64(10000) * 1e6
	buildkitPruneMax      = int64(20000) * 1e6

	fitrimIoctl = 0xC0185879 // _IOWR('X', 121, struct fstrim_range)
)

// Seams for tests.
var (
	procFilesystemsPath = "/proc/filesystems"
	trimPathFn          = fitrimPath
	statfsTypeFn        = statfsType
)

// snapshotterForFS picks buildkitd's snapshotter for a state dir whose
// filesystem has the given statfs magic. overlayfs only works on a real
// block-backed fs (ext4/xfs); virtiofs (FUSE), overlay and tmpfs get native.
func snapshotterForFS(magic int64, overlayAvailable bool) string {
	if !overlayAvailable {
		return snapshotterNative
	}
	switch magic {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC:
		return snapshotterOverlayfs
	}
	return snapshotterNative
}

func statfsType(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}

func kernelHasOverlay() bool {
	b, err := os.ReadFile(procFilesystemsPath)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[len(f)-1] == "overlay" {
			return true
		}
	}
	return false
}

// selectSnapshotter probes the buildkit state dir at runtime.
func selectSnapshotter(stateDir string) string {
	magic, err := statfsTypeFn(stateDir)
	if err != nil {
		log.Printf("in-guest build: statfs %s: %v; using native snapshotter", stateDir, err)
		return snapshotterNative
	}
	s := snapshotterForFS(magic, kernelHasOverlay())
	log.Printf("in-guest build: %s fs magic 0x%x -> %s snapshotter", stateDir, magic, s)
	return s
}

// buildkitdArgs is the buildkitd argv (pure, for tests).
func buildkitdArgs(root, sock, snapshotter, runcPath string) []string {
	return []string{
		"--root", root,
		"--addr", "unix://" + sock,
		"--oci-worker-snapshotter=" + snapshotter,
		"--oci-worker-gc=true",
		"--oci-worker-gc-keepstorage=" + buildkitGCKeepStorage,
		"--oci-worker-binary=" + runcPath,
		"--oci-worker-net=host",
	}
}

// pruneBuildkitCache runs the GC policy now; buildkitd's own GC is throttled
// and would never fire before the builder VM is torn down. Non-fatal.
func pruneBuildkitCache(sock string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := client.New(ctx, "unix://"+sock)
	if err != nil {
		log.Printf("in-guest build: prune: connect: %v", err)
		return
	}
	defer c.Close()
	ch := make(chan client.UsageInfo)
	done := make(chan struct{})
	var freed int64
	go func() {
		defer close(done)
		for u := range ch {
			freed += u.Size
		}
	}()
	err = c.Prune(ctx, ch, client.WithKeepOpt(0, buildkitPruneReserved, buildkitPruneMax, 0))
	close(ch)
	<-done
	if err != nil {
		log.Printf("in-guest build: prune: %v", err)
		return
	}
	log.Printf("in-guest build: pruned %d bytes of build cache", freed)
}

type fstrimRange struct{ Start, Len, MinLen uint64 }

// fitrimPath issues FITRIM over the whole filesystem at path. Mirrors
// cmd/nexus-agent/fstrim_linux.go:fitrimMount (that is package main).
func fitrimPath(path string) (uint64, error) {
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
	return rng.Len, nil
}

// trimBuildkitState FITRIMs the cache disk so freed blocks reach the host's
// sparse backing file. Only meaningful on a block-backed persistent mount.
func trimBuildkitState(path string) {
	n, err := trimPathFn(path)
	if err != nil {
		log.Printf("in-guest build: fitrim %s: %v (non-fatal)", path, err)
		return
	}
	log.Printf("in-guest build: fitrim %s trimmed %d bytes", path, n)
}

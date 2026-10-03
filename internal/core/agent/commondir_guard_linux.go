//go:build linux

package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var commonDirGuardInterval = 500 * time.Millisecond

// guardNames are the common-dir entries the guest must not write.
var guardNames = []string{"config", "hooks"}

// roMountedPaths returns the mount points in mountinfo that carry the ro flag.
func roMountedPaths(mountinfo string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(mountinfo, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		for _, o := range strings.Split(f[5], ",") {
			if o == "ro" {
				out[f[4]] = true
			}
		}
	}
	return out
}

// assertCommonDirGuard (re)binds config and hooks/ read-only. A host that
// rename-replaces config makes the guest kernel drop the file bind mount on
// virtiofs revalidation, so this runs periodically, not once.
func assertCommonDirGuard(commonDir string) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return
	}
	ro := roMountedPaths(string(data))
	for _, name := range guardNames {
		p := filepath.Join(commonDir, name)
		if ro[p] {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := guestBindMountFn(p, p, syscall.MS_RDONLY); err != nil {
			slog.Warn("commondir guard: bind ro failed", "path", p, "err", err)
		}
	}
}

func startCommonDirGuard(commonDir string) {
	assertCommonDirGuard(commonDir)
	go func() {
		for range time.Tick(commonDirGuardInterval) {
			assertCommonDirGuard(commonDir)
		}
	}()
}

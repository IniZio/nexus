package builder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// PrivateRootfsName is the file name given to the per-build clone of the
// shared builder rootfs template inside the build work directory.
const PrivateRootfsName = "builder-rootfs.ext4"

// PrivateRootfs clones the shared builder rootfs template at sharedImage into
// workDir and returns the path of the clone.
//
// # Why a clone is required
//
// builderimage.EnsureBuilderImage returns ONE cached ext4 image per
// (OCI digest, agent) pair, shared by every build on the host. The builder VM
// boots that image as /dev/vda with `root=/dev/vda rw`, so cloud-hypervisor
// opens it for writing and takes an exclusive flock on it. A second builder VM
// started while the first still runs is refused by CH at vm.boot:
//
//	500 ["Error from API","The VM could not boot",
//	     "Error locking disk images: Another instance likely holds a lock",
//	     "Failed to get Write lock for disk image: …/nexus-builder-…ext4",
//	     "The file is already locked"]
//
// The supervisor dies on that error before it writes supervisor.pid, so the
// CLI reports only "process exited before writing pidfile" and leaves an empty
// supervisor state dir. Staggering the spawns cannot help: the first VM holds
// the lock for the whole build, not for a startup window.
//
// Cloning per build makes the shared image a read-only template and gives each
// builder VM a private, writable rootfs, which is the same discipline the
// sandbox create path already applies to sandbox rootfs images.
//
// The clone uses `cp --reflink=auto --sparse=always`: a free extent clone on
// btrfs/XFS, a sparse full copy elsewhere. workDir is the caller's ephemeral
// per-build directory, so the clone is removed with it.
func PrivateRootfs(ctx context.Context, sharedImage, workDir string) (string, error) {
	if sharedImage == "" {
		return "", fmt.Errorf("builder rootfs clone: empty source image path")
	}
	if workDir == "" {
		return "", fmt.Errorf("builder rootfs clone: empty work dir")
	}
	if _, err := os.Stat(sharedImage); err != nil {
		return "", fmt.Errorf("builder rootfs clone: stat template %s: %w", sharedImage, err)
	}

	dst := filepath.Join(workDir, PrivateRootfsName)
	// cp refuses to clone onto an existing file with --reflink; remove any
	// leftover from an aborted earlier attempt in the same work dir.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("builder rootfs clone: remove stale %s: %w", dst, err)
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "cp", "--reflink=auto", "--sparse=always", sharedImage, dst)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(dst) // do not leave a partial image behind
		return "", fmt.Errorf("builder rootfs clone: cp --reflink=auto --sparse=always %s → %s: %w: %s",
			sharedImage, dst, err, stderr.String())
	}
	return dst, nil
}

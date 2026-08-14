package cli

// shadowdisk.go — heavy-write directory shadow disk infrastructure (D-DC-10).
//
// Shadow disks are guest-local virtio-blk ext4 volumes that overlay specific
// subdirectories of the captured workspace.  They serve two goals:
//
//  1. Performance — large write-heavy trees (node_modules, dist, Cargo target/)
//     benefit from the same pattern Docker recommends for anonymous volumes:
//     keep them on a separate writable layer so the base image stays clean and
//     copy-on-write amplification is avoided.
//
//  2. OOM safety — keeping bulk build artifacts off the captured workspace disk
//     so WorktreeToDisk stays below DefaultCaptureMaxBytes (2 GiB) even when
//     those directories are present on the host working tree.
//
// Device-letter contract (critical — see orchestrator note in S-HEAVY-WRITE-DISKS):
//
//  Shadow disks occupy ExtraDisks[0..N-1] in the order returned by
//  buildShadowDiskSpecs.  The workspace disk is appended LAST by
//  service.CreateAndBoot (after all caller-supplied ExtraDisks).
//  Mapping:
//    ExtraDisks[0]   → /dev/vdb   (shadow disk 0)
//    ExtraDisks[1]   → /dev/vdc   (shadow disk 1)
//    …
//    ExtraDisks[N-1] → /dev/vd{b+N-1}  (shadow disk N-1)
//    ExtraDisks[N]   → /dev/vd{b+N}    (workspace, appended by service)
//
//  All device paths are derived from the actual ExtraDisks index.
//  NOTHING is hardcoded.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/service"
)

// DefaultShadowDirs is the documented default set of workspace-relative
// subdirectory names that are backed by guest-local shadow disks instead of
// sitting on the captured workspace volume.
//
// Only TOP-LEVEL paths relative to the workspace root are listed.  Each entry
// produces exactly one virtio-blk disk.  For monorepos that contain nested
// copies (e.g. packages/web/node_modules) callers must supply an explicit
// override slice; each nested path becomes its own disk.  The number of
// shadow disks is bounded by the number of available virtio-blk letters
// (practical limit: ~24 disks before the kernel runs out of letters).
//
// The Go build cache ($GOCACHE, typically ~/.cache/go-build) lives outside
// the workspace root and is therefore NOT shadowed here; it is managed via
// the builder cache-disk mechanism (SelectCacheDisks).
var DefaultShadowDirs = []string{
	"node_modules", // JavaScript / TypeScript package tree
	".next",        // Next.js build output
	"target",       // Rust / Cargo build artifacts
	"dist",         // generic transpile / bundle output
}

// defaultShadowDiskSizeBytes is the preallocated size of each shadow ext4
// disk.  Sparse allocation means host disk usage equals only what the guest
// writes; 10 GiB covers the vast majority of node_modules and Cargo target/
// trees without requiring the host to have 10 GiB free.
const defaultShadowDiskSizeBytes int64 = 10 << 30 // 10 GiB

// ShadowDisk describes a single heavy-write directory backed by a guest-local
// virtio-blk ext4 volume.
type ShadowDisk struct {
	// RelDir is the path relative to the workspace root (e.g. "node_modules"
	// or "packages/web/node_modules").
	RelDir string

	// HostPath is the absolute path to the sparse ext4 image on the host.
	// Passed as an ExtraDisk entry to service.CreateAndBoot.
	HostPath string

	// GuestTarget is the absolute mount point inside the VM,
	// equal to WorkspaceSpec.GuestPath + "/" + RelDir.
	GuestTarget string
}

// buildShadowDiskSpecs computes a ShadowDisk descriptor for each entry in
// dirs.  diskDir is the host directory where ext4 images will be created;
// guestPath is the guest-side workspace root (WorkspaceSpec.GuestPath).
//
// All dirs in the provided list are included; existence in the source tree
// is NOT required — shadow disks are created regardless so the guest always
// has a writable overlay on those paths.
//
// Invalid entries (absolute paths, ".") are silently skipped.
func buildShadowDiskSpecs(dirs []string, diskDir, guestPath string) []ShadowDisk {
	specs := make([]ShadowDisk, 0, len(dirs))
	for _, rel := range dirs {
		rel = filepath.Clean(rel)
		if filepath.IsAbs(rel) || rel == "." {
			continue
		}
		// Flatten path separators into underscores for the host filename so
		// nested paths (packages/web/node_modules) produce a safe filename.
		safeName := strings.ReplaceAll(rel, string(filepath.Separator), "_")
		specs = append(specs, ShadowDisk{
			RelDir:      rel,
			HostPath:    filepath.Join(diskDir, safeName+".shadow.ext4"),
			GuestTarget: guestPath + "/" + rel,
		})
	}
	return specs
}

// createShadowDisk preallocates a sparse file at spec.HostPath and formats it
// as an empty ext4 filesystem ready for read-write use by the guest.
//
// Requires mke2fs on the host PATH.
func createShadowDisk(ctx context.Context, spec ShadowDisk) error {
	if err := preallocateFile(spec.HostPath, defaultShadowDiskSizeBytes); err != nil {
		return fmt.Errorf("shadow disk %s: preallocate: %w", spec.RelDir, err)
	}
	if err := formatExt4(ctx, spec.HostPath); err != nil {
		_ = os.Remove(spec.HostPath) // clean up the sparse file on format failure
		return fmt.Errorf("shadow disk %s: format ext4: %w", spec.RelDir, err)
	}
	return nil
}

// ErrShadowMke2fsUnavailable is returned when mke2fs is not on the host PATH.
var ErrShadowMke2fsUnavailable = fmt.Errorf("shadow disk: mke2fs not found on PATH (install e2fsprogs)")

// formatExt4 formats the file at path as an empty ext4 filesystem using mke2fs.
func formatExt4(ctx context.Context, path string) error {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		return ErrShadowMke2fsUnavailable
	}
	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-F",                              // force (no interactive confirmation)
		"-E", "lazy_itable_init=0,lazy_journal_init=0", // fully initialize inode table
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shadowExtraDisks converts ShadowDisk specs to service.ExtraDisk entries
// in the same order as specs.  The i-th entry maps to ExtraDisks[i] and
// therefore to /dev/vd{b+i} in the guest.
func shadowExtraDisks(specs []ShadowDisk) []service.ExtraDisk {
	eds := make([]service.ExtraDisk, len(specs))
	for i, s := range specs {
		eds[i] = service.ExtraDisk{Path: s.HostPath}
	}
	return eds
}

// shadowDevicePath returns the guest block-device path for ExtraDisks[index].
//
//	ExtraDisks[0] → /dev/vdb  (index 0)
//	ExtraDisks[1] → /dev/vdc  (index 1)
//	…
//
// /dev/vda is always the rootfs and is never in ExtraDisks.
func shadowDevicePath(index int) string {
	return "/dev/vd" + string(rune('b'+index))
}

// shadowGuestMounts computes agent.GuestMount entries for shadow disks.
//
// extraDisksOffset is the number of caller-supplied ExtraDisks that precede
// the shadow disks in the final ExtraDisks slice passed to CreateAndBoot.
// In the current wiring (shadow disks are prepended to ExtraDisks before the
// workspace; no other caller-supplied ExtraDisks are present), offset is 0.
// The parameter is explicit to make the device-letter derivation auditable.
//
// Device mapping:
//
//	shadow disk i at extraDisksOffset+i → /dev/vd{b+extraDisksOffset+i}
func shadowGuestMounts(specs []ShadowDisk, extraDisksOffset int) []agent.GuestMount {
	mounts := make([]agent.GuestMount, len(specs))
	for i, s := range specs {
		mounts[i] = agent.GuestMount{
			Device: shadowDevicePath(extraDisksOffset + i),
			Target: s.GuestTarget,
			FSType: "ext4",
		}
	}
	return mounts
}

// WorkspaceGuestMount returns the agent.GuestMount for the workspace disk.
//
// The workspace disk is appended LAST to ExtraDisks by service.CreateAndBoot,
// after all caller-supplied ExtraDisks (including N shadow disks at [0..N-1]).
// It therefore occupies ExtraDisks[N] and maps to /dev/vd{b+N}.
//
// This function is the authoritative derivation of the workspace device path:
// callers MUST use it rather than hardcoding /dev/vdb or any fixed letter.
func WorkspaceGuestMount(guestPath string, numShadowDisks int) agent.GuestMount {
	return agent.GuestMount{
		Device: shadowDevicePath(numShadowDisks), // ExtraDisks[N] → /dev/vd{b+N}
		Target: guestPath,
		FSType: "ext4",
	}
}

// makeShadowExcludeCapturer returns a WorkspaceCapturer that excludes shadow
// directory names from the captured workspace image.
//
// Without this exclusion, directories such as node_modules (if already
// present in the source tree from a prior npm install) would be included in
// the workspace capture, inflating the ext4 image and potentially triggering
// the DefaultCaptureMaxBytes OOM guard — defeating the structural mitigation
// that shadow disks provide.
//
// Exclusion is performed by passing shadowDirs as extra patterns to
// builder.WorktreeToDiskWithExtra. The user's source tree — including
// .dockerignore — is never read from or written to by this function.
//
// Timing is recorded via slog at the end of the capture for diagnostics
// (requirement 5: record measured capture wall-clock).
func makeShadowExcludeCapturer(shadowDirs []string) func(context.Context, string, string, int64) error {
	return func(ctx context.Context, srcDir, outExt4 string, maxBytes int64) error {
		start := time.Now()
		err := builder.WorktreeToDiskWithExtra(ctx, srcDir, outExt4, maxBytes, shadowDirs)
		elapsed := time.Since(start)
		slog.Info("workspace capture complete",
			"elapsed", elapsed.Round(time.Millisecond),
			"shadow_dirs_excluded", true,
			"err", err,
		)
		return err
	}
}

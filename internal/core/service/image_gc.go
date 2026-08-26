package service

import (
	"context"
	"fmt"
	"sort"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/image"
)

// DefaultGCFreeSpaceFloorGiB is the minimum free disk space (in GiB) required
// on the filesystem backing the nexus3 state directory before a build is allowed
// to start. When free space falls below this floor, automatic GC runs first.
// Configurable via ImageGCConfig.FreeSpaceFloorGiB in nexus3.yaml or
// ~/.config/nexus3/config.yaml.
const DefaultGCFreeSpaceFloorGiB = 15

// SandboxImageLister is the subset of store.Store required by image GC to
// enumerate existing sandbox records and extract their image references.
type SandboxImageLister interface {
	List(ctx context.Context) ([]domain.Sandbox, error)
}

// freeSpaceFunc returns the number of available bytes on the filesystem
// containing path. Overridden in tests via SetFreeSpaceFuncForTest.
var freeSpaceFunc = func(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("image gc: statfs %q: %w", path, err)
	}
	// Bavail is blocks available to unprivileged users; Bsize is the block size.
	return st.Bavail * uint64(st.Bsize), nil
}

// SetFreeSpaceFuncForTest replaces the free-space probe with fn for the
// duration of a test. Restore the original with a deferred call to the
// returned cleanup function.
func SetFreeSpaceFuncForTest(fn func(string) (uint64, error)) func() {
	orig := freeSpaceFunc
	freeSpaceFunc = fn
	return func() { freeSpaceFunc = orig }
}

// ReferencedDigests returns the set of image digests that must be preserved
// during GC. The returned set includes:
//   - All KindBase images (the nexus3-agent-base rootfs and any siblings).
//   - Every image referenced by an existing sandbox record (Envelope.ImageDigest).
//   - Any additional digests passed in extra (e.g. the image just built).
//
// Ambiguity resolves to KEEP: a sandbox whose ImageDigest cannot be validated
// as a domain.Digest is silently skipped (non-parseable strings were never
// stored in the content-addressed cache). A valid digest that does not exist
// in the cache is silently ignored by Prune.
//
// store may be nil; passing nil disables sandbox-reference tracking and only
// KindBase images plus extra digests are kept.
func ReferencedDigests(ctx context.Context, c *image.Cache, store SandboxImageLister, extra ...domain.Digest) ([]domain.Digest, error) {
	all, err := c.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("image gc: list cache: %w", err)
	}

	ref := make(map[domain.Digest]struct{})

	// Always keep all KindBase images (nexus3-agent-base and any siblings).
	for _, img := range all {
		if img.Kind == domain.KindBase {
			ref[img.Digest] = struct{}{}
		}
	}

	// Keep images referenced by existing sandbox records.
	if store != nil {
		sandboxes, err := store.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("image gc: list sandboxes: %w", err)
		}
		for _, sb := range sandboxes {
			d := sb.Envelope.ImageDigest
			if d == "" {
				continue
			}
			parsed, parseErr := domain.ParseDigest(d)
			if parseErr != nil {
				// Cannot parse — ImageDigest may be a raw ext4 path or legacy value.
				// Skip: non-parseable values were never stored as cache digests.
				continue
			}
			ref[parsed] = struct{}{}
		}
	}

	// Keep any explicitly pinned extra digests (e.g. the just-built image).
	for _, d := range extra {
		if d.Valid() {
			ref[d] = struct{}{}
		}
	}

	out := make([]domain.Digest, 0, len(ref))
	for d := range ref {
		out = append(out, d)
	}
	return out, nil
}

// AutoPruneAfterBuild prunes orphan images from the cache after a successful
// build, retaining the referenced set plus up to keepNewestBuilder of the most
// recently created KindBuilder images. It is called immediately after a
// successful --file build so that stale prior builder artifacts are reclaimed
// while the 2+ currently-running sandbox images are always preserved.
//
// keepNewestBuilder == 0 disables the retention budget (only sandbox-referenced
// and base images are kept; the just-built image is protected via extra).
func AutoPruneAfterBuild(ctx context.Context, c *image.Cache, store SandboxImageLister, keepNewestBuilder int, extra ...domain.Digest) (int, error) {
	ref, err := ReferencedDigests(ctx, c, store, extra...)
	if err != nil {
		return 0, err
	}

	// Optional retention budget: keep up to keepNewestBuilder of the most
	// recently created KindBuilder images even if no sandbox references them.
	if keepNewestBuilder > 0 {
		all, listErr := c.List(ctx)
		if listErr != nil {
			return 0, fmt.Errorf("image gc: list for retention budget: %w", listErr)
		}

		// Build a set of already-kept digests so we don't double-count.
		refSet := make(map[domain.Digest]struct{}, len(ref))
		for _, d := range ref {
			refSet[d] = struct{}{}
		}

		// Collect unreferenced builder images.
		var builders []domain.Image
		for _, img := range all {
			if img.Kind == domain.KindBuilder {
				if _, kept := refSet[img.Digest]; !kept {
					builders = append(builders, img)
				}
			}
		}

		// Sort newest-first by creation time.
		sort.Slice(builders, func(i, j int) bool {
			return builders[i].CreatedAt.After(builders[j].CreatedAt)
		})
		for i, img := range builders {
			if i >= keepNewestBuilder {
				break
			}
			ref = append(ref, img.Digest)
		}
	}

	n, err := c.Prune(ctx, ref)
	if err != nil {
		return 0, fmt.Errorf("image gc: prune: %w", err)
	}
	return n, nil
}

// BuildPreflight checks whether the filesystem at stateDir has at least
// floorBytes of free space. When free space is below the floor it runs a
// referenced-prune first. If space is still insufficient after the prune it
// returns a clear error instead of letting buildkit fail cryptically.
//
// A failure to measure free space is treated as non-fatal (returns nil) so
// that builds proceed on systems where statfs is unavailable or permission is
// denied. A failure to run the preflight prune IS returned because it indicates
// an image cache problem.
func BuildPreflight(ctx context.Context, stateDir string, floorBytes uint64, c *image.Cache, store SandboxImageLister) error {
	free, err := freeSpaceFunc(stateDir)
	if err != nil {
		// Cannot measure — proceed and let buildkit surface a disk-full error.
		return nil
	}
	if free >= floorBytes {
		return nil // plenty of space
	}

	// Below floor: prune orphan images first.
	ref, refErr := ReferencedDigests(ctx, c, store)
	if refErr != nil {
		return fmt.Errorf("build preflight: compute referenced digests: %w", refErr)
	}
	pruned, pruneErr := c.Prune(ctx, ref)
	if pruneErr != nil {
		return fmt.Errorf("build preflight: prune: %w", pruneErr)
	}

	// Re-measure after prune.
	free2, err2 := freeSpaceFunc(stateDir)
	if err2 != nil {
		return nil // can't re-measure; proceed
	}
	if free2 >= floorBytes {
		return nil // enough space after prune
	}

	shortfall := floorBytes - free2
	return fmt.Errorf(
		"insufficient host disk space: need %d GiB free, have %.1f GiB"+
			" (pruned %d orphan image(s), still short by %.1f GiB)"+
			" — free disk space or lower image.free_space_floor_gib in nexus3.yaml",
		floorBytes>>30,
		float64(free2)/(1<<30),
		pruned,
		float64(shortfall)/(1<<30),
	)
}

package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/diskname"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// parentShadowDiskNames returns the basenames of every shadow disk in diskDir
// belonging to the sandbox with the given handle.
//
// Correlation is by the safeHandle embedded in the filename, which is how the
// reaper does it — matching on anything else would protect a different set of
// files than the one at risk. Legacy pre-B1 shadow disks (*.shadow.ext4) carry
// no handle, match no sandbox, and are deliberately excluded: they are
// unconditionally reclaimable and leasing them would resurrect garbage.
//
// A missing diskDir is not an error: a sandbox with no shadow disks has
// nothing to protect.
func parentShadowDiskNames(diskDir, handle string) ([]string, error) {
	entries, err := os.ReadDir(diskDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", diskDir, err)
	}
	want := SafeHandle(handle)
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		got, ok := diskname.ShadowDiskSafeHandle(e.Name())
		if !ok || got != want {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// childShadowPlan returns the shadow-intent handle and the planned copy paths
// for one fork child, given the parent's shadow disk basenames.
//
// The naming must match cloudhypervisor.ChildExtraDiskPath exactly — it
// prefixes each non-workspace extra disk with "<childID>-" — because the
// reaper reads the handle back out of the filename. If the two ever drift the
// intent protects a path nothing writes, and the real copy is unguarded again
// with no test failing. TestChildShadowPlan_MatchesDriverNaming pins them
// together against the driver's own function.
func childShadowPlan(diskDir string, childID domain.SandboxID, parentNames []string) (handle string, paths []string) {
	prefix := childID.String() + "-"
	paths = make([]string, 0, len(parentNames))
	for _, name := range parentNames {
		paths = append(paths, filepath.Join(diskDir, prefix+name))
	}
	// Every child copy carries the same handle — <childULID>-<parentSafeHandle>
	// — because ShadowDiskSafeHandle takes the token before the first
	// ".shadow.", and the prefix is glued onto the parent's safeHandle.
	if len(parentNames) == 0 {
		return "", nil
	}
	h, ok := diskname.ShadowDiskSafeHandle(prefix + parentNames[0])
	if !ok {
		return "", nil
	}
	return h, paths
}

// leaseForkChildren publishes, for every child, the two intents that keep a
// concurrent `reap --apply` off its disks until its record commits:
//
//   - a ULID-keyed create intent covering <childID>.raw, and
//   - a handle-keyed shadow intent covering the child's copies of the
//     parent's shadow disks.
//
// BOTH are required and neither substitutes for the other. The reaper
// correlates .raw by ULID and shadow disks by handle, so a single intent
// cannot answer both questions — that keying mismatch is what made TBD-PD-25
// and TBD-PD-38 the same bug in two places.
//
// The shadow intent is skipped when the parent has no shadow disks, because an
// intent naming paths nothing will write is a file the reaper enumerates and
// an operator has to explain.
//
// On error the caller's deferred drain releases whatever was published; the
// partial maps are returned for exactly that reason.
func leaseForkChildren(
	diskDir, parentHandle string,
	childIDs []domain.SandboxID,
) (map[domain.SandboxID]*createIntentLease, map[domain.SandboxID]*ShadowIntentLease, error) {
	leases := make(map[domain.SandboxID]*createIntentLease, len(childIDs))
	shadowLeases := make(map[domain.SandboxID]*ShadowIntentLease, len(childIDs))

	parentShadowNames, err := parentShadowDiskNames(diskDir, parentHandle)
	if err != nil {
		return leases, shadowLeases, fmt.Errorf("scan parent shadow disks: %w", err)
	}

	for _, id := range childIDs {
		lease, lErr := writeCreateIntent(diskDir, id, filepath.Join(diskDir, id.String()+".raw"), "")
		if lErr != nil {
			return leases, shadowLeases, fmt.Errorf("lease child %s: %w", id, lErr)
		}
		leases[id] = lease

		if len(parentShadowNames) == 0 {
			continue
		}
		handle, paths := childShadowPlan(diskDir, id, parentShadowNames)
		sLease, sErr := WriteShadowIntent(diskDir, handle, paths)
		if sErr != nil {
			return leases, shadowLeases, fmt.Errorf("lease child %s shadow disks: %w", id, sErr)
		}
		shadowLeases[id] = sLease
	}
	return leases, shadowLeases, nil
}

// releaseChildLeases drops both of one child's leases and forgets them, so the
// caller's deferred drain does not double-release. Releasing removes the
// intent file, which is why this must happen only after the record commits.
func releaseChildLeases(
	leases map[domain.SandboxID]*createIntentLease,
	shadowLeases map[domain.SandboxID]*ShadowIntentLease,
	id domain.SandboxID,
) {
	if l, ok := leases[id]; ok {
		l.release()
		delete(leases, id)
	}
	if l, ok := shadowLeases[id]; ok {
		l.Release()
		delete(shadowLeases, id)
	}
}

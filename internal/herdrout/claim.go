package herdrout

import (
	"os"
	"path/filepath"
	"strings"
)

// ClaimsDirName is the directory under the nexus store root that holds
// per-branch controller-claim markers.
const ClaimsDirName = "controller-wt-claims"

// ClaimWorktree writes the controller-claim marker for branch under storeRoot
// so herdr's on-worktree-created hook skips auto-provisioning. It returns a
// release func that removes the marker; release is always non-nil and safe to
// defer. Marker write failures are non-fatal: release is then a no-op.
func ClaimWorktree(storeRoot, branch string) (release func()) {
	noop := func() {}
	if storeRoot == "" || branch == "" {
		return noop
	}
	dir := filepath.Join(storeRoot, ClaimsDirName)
	if os.MkdirAll(dir, 0o755) != nil {
		return noop
	}
	path := filepath.Join(dir, strings.ReplaceAll(branch, "/", "-"))
	if os.WriteFile(path, []byte{}, 0o644) != nil {
		return noop
	}
	return func() { _ = os.Remove(path) }
}

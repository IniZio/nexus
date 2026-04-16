package lima

import (
	"fmt"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime/drivers/shared"
)

// btrfsForkScript returns a shell script that forks parentPath to childPath.
//
// It prefers btrfs subvolume snapshot (O(1) copy-on-write) when both paths
// are on a btrfs filesystem. When the filesystem is not btrfs (e.g. virtiofs
// passthrough from the host), it falls back to cp -a (plain recursive copy).
//
// The btrfs path requires both paths to be on the same btrfs filesystem.
// The cp -a fallback works on any filesystem but is O(data size).
func btrfsForkScript(parentPath, childPath string) string {
	p := shared.ShellQuote(parentPath)
	c := shared.ShellQuote(childPath)
	return fmt.Sprintf(`set -e
PARENT=%s
CHILD=%s
if sudo -n btrfs subvolume snapshot "$PARENT" "$CHILD" 2>/dev/null; then
  exit 0
fi
# Fallback: filesystem is not btrfs (e.g. virtiofs). Use plain copy.
sudo -n mkdir -p "$CHILD"
sudo -n cp -a "$PARENT/." "$CHILD/"
`, p, c)
}

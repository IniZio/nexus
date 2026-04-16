package lima

import (
	"strings"
	"testing"
)

func TestBtrfsForkScript(t *testing.T) {
	script := btrfsForkScript("/workspace/parent-id", "/workspace/child-id")
	if !strings.Contains(script, "btrfs subvolume snapshot") {
		t.Error("fork script must use btrfs subvolume snapshot")
	}
	if !strings.Contains(script, "/workspace/parent-id") {
		t.Error("fork script must reference parent path")
	}
	if !strings.Contains(script, "/workspace/child-id") {
		t.Error("fork script must reference child path")
	}
	// Must NOT use cp, rsync, or tar
	for _, bad := range []string{"cp -r", "rsync", "tar "} {
		if strings.Contains(script, bad) {
			t.Errorf("fork script must not use %q", bad)
		}
	}
}

package supervisor

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/resize"
)

func TestBackfillRootDiskIndex_PrependsMissing(t *testing.T) {
	got := backfillRootDiskIndex([]int{0}, false)
	if len(got) != 2 || got[0] != resize.RootDiskIndex || got[1] != 0 {
		t.Errorf("got %v, want [%d 0]", got, resize.RootDiskIndex)
	}
}

func TestBackfillRootDiskIndex_NoDuplicate(t *testing.T) {
	in := []int{resize.RootDiskIndex, 0}
	got := backfillRootDiskIndex(in, false)
	if len(got) != 2 {
		t.Errorf("got %v (len=%d), want no duplicate", got, len(got))
	}
}

func TestBackfillRootDiskIndex_EphemeralUnchanged(t *testing.T) {
	got := backfillRootDiskIndex([]int{0}, true)
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("ephemeral: got %v, want [0]", got)
	}
}

func TestBackfillRootDiskIndex_EmptyNonEphemeral(t *testing.T) {
	got := backfillRootDiskIndex([]int{}, false)
	if len(got) != 1 || got[0] != resize.RootDiskIndex {
		t.Errorf("empty non-ephemeral: got %v, want [%d]", got, resize.RootDiskIndex)
	}
}

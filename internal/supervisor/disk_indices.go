package supervisor

import (
	"slices"

	"github.com/IniZio/nexus/internal/core/resize"
)

func backfillRootDiskIndex(indices []int, ephemeral bool) []int {
	if ephemeral || slices.Contains(indices, resize.RootDiskIndex) {
		return indices
	}
	return append([]int{resize.RootDiskIndex}, indices...)
}

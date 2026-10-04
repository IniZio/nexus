package cloudhypervisor

import (
	"fmt"

	"github.com/IniZio/nexus/internal/core/driver"
)

// RestoreMode is driver.RestoreMode: one enum for the service and the VMM.
// The values are CH's memory_restore_mode spellings (lowercase; confirmed by
// the v52 CLI help "memory_restore_mode=copy|ondemand").
type RestoreMode = driver.RestoreMode

const (
	// RestoreModeCopy eagerly reads snapshot memory into RAM (CH default).
	RestoreModeCopy = driver.RestoreModeCopy
	// RestoreModeOnDemand lazily pages memory in via userfaultfd.
	RestoreModeOnDemand = driver.RestoreModeOnDemand
)

// userfaultfdDevice is the device ProbeOnDemandRestore checks by default.
const userfaultfdDevice = "/dev/userfaultfd"

// ParseRestoreMode maps "" and "copy" to copy and "ondemand" to ondemand.
func ParseRestoreMode(s string) (RestoreMode, error) {
	switch RestoreMode(s) {
	case "", RestoreModeCopy:
		return RestoreModeCopy, nil
	case RestoreModeOnDemand:
		return RestoreModeOnDemand, nil
	}
	return "", fmt.Errorf("cloudhypervisor: unknown restore mode %q (want copy|ondemand)", s)
}

// ProbeOnDemandRestore reports whether ondemand restore can work on this host:
// /dev/userfaultfd or the userfaultfd(2) syscall must work and support UFFD_FEATURE_MISSING_SHMEM (memfd
// shared memory). Callers refuse ondemand on error instead of failing mid-restore.
func ProbeOnDemandRestore() error { return probeOnDemandRestoreAny(userfaultfdDevice) }

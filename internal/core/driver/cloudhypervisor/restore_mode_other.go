//go:build !linux

package cloudhypervisor

import "errors"

func probeOnDemandRestore(string) error {
	return errors.New("cloudhypervisor: ondemand restore unavailable: userfaultfd requires linux")
}

func probeOnDemandRestoreAny(dev string) error { return probeOnDemandRestore(dev) }

//go:build !linux

package cloudhypervisor

// nestedVirtPreflight off Linux always fails; nested KVM requires Linux.
func nestedVirtPreflight() error {
	return errUnsupportedPlatform
}

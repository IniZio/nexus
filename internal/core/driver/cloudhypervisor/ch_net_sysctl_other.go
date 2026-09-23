//go:build !linux

package cloudhypervisor

func setIfaceForwardingNetlink(iface string) error {
	return errUnsupportedPlatform
}

func provenForwardingZero(iface string) error {
	return errUnsupportedPlatform
}

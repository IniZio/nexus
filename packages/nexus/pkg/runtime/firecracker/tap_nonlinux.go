//go:build !linux

package firecracker

import (
	"errors"
	"fmt"
)

const bridgeName = "nexusbr0"

const bridgeGatewayIP = "172.26.0.1"

const guestSubnetCIDR = "172.26.0.0/16"

func tapNameForWorkspace(workspaceID string) string {
	suffix := workspaceID
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return "nx-" + suffix
}

func checkTapHelper() error {
	return errors.New("tap helper is only available on Linux")
}

func checkBridge() error {
	return errors.New("bridge is only available on Linux")
}

func tapHelperSetupInstructions() string {
	return "not applicable on non-Linux systems"
}

func bridgeSetupInstructions() string {
	return "not applicable on non-Linux systems"
}

func realSetupTAP(tapName, hostIP, subnetCIDR string) (any, error) {
	return nil, fmt.Errorf("TAP devices are only supported on Linux")
}

func realTeardownTAP(tapName, subnetCIDR string) {
}

//go:build linux

package firecracker

import (
	"fmt"
	"os/exec"
	"strings"
)

// bridgeName is the Linux bridge all Firecracker tap devices are attached to.
const bridgeName = "nexusbr0"

// bridgeGatewayIP is the host-side IP on the bridge (default gateway for guests).
const bridgeGatewayIP = "172.26.0.1"

// guestSubnetCIDR is the subnet behind the bridge.
const guestSubnetCIDR = "172.26.0.0/16"

// tapHelperBin is the name of the privileged tap helper binary.
const tapHelperBin = "nexus-tap-helper"

// checkTapHelper verifies that nexus-tap-helper is installed and has cap_net_admin.
// Returns an error with setup instructions if not found or not configured.
func checkTapHelper() error {
	path, err := exec.LookPath(tapHelperBin)
	if err != nil {
		return fmt.Errorf(
			"%s not found in PATH\n\nOne-time setup required:\n%s",
			tapHelperBin, tapHelperSetupInstructions(),
		)
	}

	// Best-effort: verify cap_net_admin via getcap (skip if getcap unavailable).
	out, err := exec.Command("getcap", path).Output()
	if err != nil {
		// getcap not available — cannot verify, let runtime fail if needed.
		return nil
	}
	if !strings.Contains(string(out), "cap_net_admin") {
		return fmt.Errorf(
			"%s at %s lacks cap_net_admin\n\nRun:\n  sudo setcap cap_net_admin=ep %s",
			tapHelperBin, path, path,
		)
	}
	return nil
}

// checkBridge verifies that nexusbr0 exists and is UP.
func checkBridge() error {
	out, err := exec.Command("ip", "link", "show", bridgeName).CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"bridge %s not found\n\nOne-time setup required:\n%s",
			bridgeName, bridgeSetupInstructions(),
		)
	}
	if !strings.Contains(string(out), "UP") {
		return fmt.Errorf(
			"bridge %s exists but is not UP\n\nTry: sudo ip link set %s up\nOr re-run full setup:\n%s",
			bridgeName, bridgeName, bridgeSetupInstructions(),
		)
	}
	return nil
}

// tapHelperSetupInstructions returns the one-time setup commands for the tap helper.
func tapHelperSetupInstructions() string {
	return "  go build -o /tmp/nexus-tap-helper ./packages/nexus/cmd/nexus-tap-helper/\n" +
		"  sudo cp /tmp/nexus-tap-helper /usr/local/bin/nexus-tap-helper\n" +
		"  sudo setcap cap_net_admin=ep /usr/local/bin/nexus-tap-helper"
}

// bridgeSetupInstructions returns the one-time setup commands for the bridge.
func bridgeSetupInstructions() string {
	return "  sudo tee /etc/systemd/network/10-nexusbr0.netdev << 'EOF'\n" +
		"[NetDev]\nName=nexusbr0\nKind=bridge\nEOF\n\n" +
		"  sudo tee /etc/systemd/network/11-nexusbr0.network << 'EOF'\n" +
		"[Match]\nName=nexusbr0\n[Network]\nAddress=172.26.0.1/16\nIPForward=yes\nIPMasquerade=ipv4\nEOF\n\n" +
		"  sudo tee /etc/systemd/network/12-nexus-tap.network << 'EOF'\n" +
		"[Match]\nName=nexus-*\n[Network]\nBridge=nexusbr0\nEOF\n\n" +
		"  sudo systemctl enable --now systemd-networkd"
}

// realSetupTAP creates a tap device and attaches it to nexusbr0 via nexus-tap-helper.
func realSetupTAP(tapName, hostIP, subnetCIDR string) (any, error) {
	out, err := exec.Command(tapHelperBin, "create", tapName, bridgeName).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("nexus-tap-helper create %s: %w: %s", tapName, err, strings.TrimSpace(string(out)))
	}
	return nil, nil
}

// realTeardownTAP removes the tap device via nexus-tap-helper.
// Errors are swallowed — the tap may already be gone if the VM crashed.
func realTeardownTAP(tapName, subnetCIDR string) {
	_ = exec.Command(tapHelperBin, "delete", tapName).Run()
}

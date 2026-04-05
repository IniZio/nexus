//go:build !linux

package firecracker

import (
	"strings"
	"testing"
)

func TestTapNameForWorkspace(t *testing.T) {
	cases := []struct {
		workspaceID string
		wantPrefix  string
		maxLen      int
	}{
		{"abc", "nx-abc", 15},
		{"abcdefghijklmnopqrstuvwxyz", "nx-abcdefghijkl", 15},
		{"ws-12345678901234567890", "nx-ws-123456789", 15},
		{"short", "nx-short", 15},
	}

	for _, tc := range cases {
		got := tapNameForWorkspace(tc.workspaceID)
		if got != tc.wantPrefix {
			t.Errorf("tapNameForWorkspace(%q) = %q, want %q", tc.workspaceID, got, tc.wantPrefix)
		}
		if len(got) > tc.maxLen {
			t.Errorf("tapNameForWorkspace(%q) = %q (len %d), exceeds IFNAMSIZ-1 (%d)",
				tc.workspaceID, got, len(got), tc.maxLen)
		}
	}
}

func TestCheckTapHelperUnsupported(t *testing.T) {
	err := checkTapHelper()
	if err == nil {
		t.Fatal("expected error on non-Linux")
	}
	if !strings.Contains(err.Error(), "Linux") {
		t.Errorf("expected error to mention Linux, got: %v", err)
	}
}

func TestCheckBridgeUnsupported(t *testing.T) {
	err := checkBridge()
	if err == nil {
		t.Fatal("expected error on non-Linux")
	}
	if !strings.Contains(err.Error(), "Linux") {
		t.Errorf("expected error to mention Linux, got: %v", err)
	}
}

func TestRealSetupTAPUnsupported(t *testing.T) {
	_, err := realSetupTAP("test-tap", "172.26.0.2", "172.26.0.0/16")
	if err == nil {
		t.Fatal("expected error on non-Linux")
	}
	if !strings.Contains(err.Error(), "Linux") {
		t.Errorf("expected error to mention Linux, got: %v", err)
	}
}

func TestConstantsDefined(t *testing.T) {
	if bridgeName != "nexusbr0" {
		t.Errorf("bridgeName = %q, want %q", bridgeName, "nexusbr0")
	}
	if bridgeGatewayIP != "172.26.0.1" {
		t.Errorf("bridgeGatewayIP = %q, want %q", bridgeGatewayIP, "172.26.0.1")
	}
	if guestSubnetCIDR != "172.26.0.0/16" {
		t.Errorf("guestSubnetCIDR = %q, want %q", guestSubnetCIDR, "172.26.0.0/16")
	}
}

func TestSetupInstructionsNonLinux(t *testing.T) {
	if got := tapHelperSetupInstructions(); !strings.Contains(got, "not applicable") {
		t.Errorf("tapHelperSetupInstructions should indicate non-Linux: %v", got)
	}
	if got := bridgeSetupInstructions(); !strings.Contains(got, "not applicable") {
		t.Errorf("bridgeSetupInstructions should indicate non-Linux: %v", got)
	}
}

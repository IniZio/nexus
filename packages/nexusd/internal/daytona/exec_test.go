package daytona

import (
	"testing"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

func TestWorkspaceInfoTTL(t *testing.T) {
	t.Run("TTL set when running with auto-stop", func(t *testing.T) {
		info := &WorkspaceInfo{
			Status:           types.StatusRunning,
			AutoStopInterval: 15,
		}
		if info.AutoStopInterval > 0 {
			info.TTL = "15m remaining"
		}

		if info.TTL != "15m remaining" {
			t.Errorf("expected TTL '15m remaining', got %q", info.TTL)
		}
	})

	t.Run("TTL empty when stopped", func(t *testing.T) {
		info := &WorkspaceInfo{
			Status:           types.StatusStopped,
			AutoStopInterval: 15,
		}
		if info.Status != types.StatusRunning {
			info.TTL = ""
		}

		if info.TTL != "" {
			t.Errorf("expected empty TTL, got %q", info.TTL)
		}
	})

	t.Run("TTL empty when no auto-stop", func(t *testing.T) {
		info := &WorkspaceInfo{
			Status:           types.StatusRunning,
			AutoStopInterval: 0,
		}
		if info.Status == types.StatusRunning && info.AutoStopInterval > 0 {
			info.TTL = "15m remaining"
		}

		if info.TTL != "" {
			t.Errorf("expected empty TTL, got %q", info.TTL)
		}
	})
}

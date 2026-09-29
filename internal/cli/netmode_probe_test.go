package cli

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/service"
)

func TestTapProbeNotWiredUnderTest(t *testing.T) {
	if service.TapProbe != nil {
		t.Fatal("service.TapProbe must stay nil unless EnableTapProbe is called from main")
	}
}

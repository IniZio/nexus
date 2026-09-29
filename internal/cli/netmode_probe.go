package cli

import (
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

// EnableTapProbe wires the real create-time tap probe. Only the nexus main
// calls it; tests and library importers keep the nil (no-probe) default.
func EnableTapProbe() {
	service.TapProbe = func() error {
		root, err := store.DefaultRoot()
		if err != nil {
			return nil
		}
		return cloudhypervisor.ProbeTapCached(root)
	}
}

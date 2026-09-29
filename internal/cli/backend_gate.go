package cli

import (
	"os"
	"runtime"

	"github.com/IniZio/nexus/internal/core/driver/registry"
)

// featureBackends lists the backends that provide each backend-specific CLI
// feature. Features absent from the table are available on every backend.
var featureBackends = map[string][]string{
	"disk":                {registry.CloudHypervisor},
	"volume":              {registry.CloudHypervisor},
	"image":               {registry.CloudHypervisor},
	"kernel install":      {registry.CloudHypervisor},
	"seam":                {registry.CloudHypervisor},
	"supervisor upgrade":  {registry.CloudHypervisor},
	"supervisor backfill": {registry.CloudHypervisor},
}

// activeBackend resolves the backend from NEXUS_BACKEND, defaulting to
// cloud-hypervisor on linux and failing elsewhere.
func activeBackend() (string, error) {
	if name := os.Getenv("NEXUS_BACKEND"); name != "" {
		return registry.Resolve(name)
	}
	if runtime.GOOS == "linux" {
		return registry.CloudHypervisor, nil
	}
	return registry.Resolve("")
}

// requireBackend returns nil when the active backend is in need (or, when
// need is empty, in the featureBackends entry for feature).
func requireBackend(feature string, need ...string) error {
	if len(need) == 0 {
		need = featureBackends[feature]
	}
	active, err := activeBackend()
	if err != nil {
		return err
	}
	if len(need) == 0 {
		return nil
	}
	for _, n := range need {
		if n == active {
			return nil
		}
	}
	return registry.Unsupported(active, feature)
}

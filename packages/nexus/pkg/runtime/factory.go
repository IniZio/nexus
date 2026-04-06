package runtime

import (
	"fmt"
	"strings"
)

type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

type Factory struct {
	capabilities []Capability
	drivers      map[string]Driver
}

const linuxBackendName = "linux"

var backendCapability = map[string]string{
	linuxBackendName: "runtime.linux",
	"sandbox":        "runtime.linux",
	"firecracker":    "runtime.linux",
	"vm":             "runtime.linux",
	"lxc":            "runtime.linux",
}

func NewFactory(capabilities []Capability, drivers map[string]Driver) *Factory {
	return &Factory{
		capabilities: capabilities,
		drivers:      drivers,
	}
}

func (f *Factory) SelectDriver(requiredBackends []string, selection string, requiredCapabilities []string) (Driver, error) {
	if err := f.validateCapabilities(requiredCapabilities); err != nil {
		return nil, err
	}

	backend, err := f.selectBackend(requiredBackends, selection)
	if err != nil {
		return nil, err
	}

	driver, ok := f.drivers[backend]
	if !ok {
		return nil, fmt.Errorf("backend %q selected but driver not registered", backend)
	}

	return driver, nil
}

func (f *Factory) validateCapabilities(required []string) error {
	for _, req := range required {
		found := false
		for _, cap := range f.capabilities {
			if cap.Name == req && cap.Available {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("required capability %q is not available", req)
		}
	}
	return nil
}

func (f *Factory) selectBackend(required []string, selection string) (string, error) {
	if selection != "prefer-first" {
		return "", fmt.Errorf("unsupported selection strategy: %q", selection)
	}

	for _, backend := range required {
		normalized := normalizeRuntimeToken(backend)
		resolvedBackend := resolveBackendAlias(normalized)
		driverKey := resolvedBackend
		if _, ok := f.drivers[driverKey]; !ok {
			// Backward compatibility: allow legacy driver map keys (firecracker/lxc/vm)
			// when canonical linux backend is requested.
			if resolvedBackend == linuxBackendName {
				if _, ok := f.drivers[normalized]; ok {
					driverKey = normalized
				} else if _, ok := f.drivers["firecracker"]; ok {
					driverKey = "firecracker"
				} else {
					continue
				}
			} else {
				continue
			}
		}

		capName := backendCapabilityName(normalized)
		if !f.isCapabilityAvailable(capName) {
			if resolvedBackend == linuxBackendName {
				legacyCapName := "runtime." + normalized
				if normalized != "" && f.isCapabilityAvailable(legacyCapName) {
					return driverKey, nil
				}
				if f.isCapabilityAvailable("runtime.firecracker") {
					return driverKey, nil
				}
			}
			continue
		}
		return driverKey, nil
	}

	return "", fmt.Errorf("no required backend available from: %v", required)
}

func resolveBackendAlias(backend string) string {
	switch normalized := normalizeRuntimeToken(backend); normalized {
	case "firecracker", "vm", "lxc", "sandbox":
		return linuxBackendName
	default:
		return normalized
	}
}

func backendCapabilityName(backend string) string {
	normalized := normalizeRuntimeToken(backend)
	if name, ok := backendCapability[normalized]; ok {
		return name
	}
	return "runtime." + normalized
}

func normalizeRuntimeToken(backend string) string {
	if backend == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(backend))
}

func (f *Factory) isCapabilityAvailable(name string) bool {
	for _, cap := range f.capabilities {
		if cap.Name == name {
			return cap.Available
		}
	}
	return false
}

func (f *Factory) Capabilities() []Capability {
	return f.capabilities
}

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

	for _, req := range required {
		candidates := f.expandRuntimeRequirement(req)
		if len(candidates) == 0 {
			continue
		}
		for _, backend := range candidates {
			if _, ok := f.drivers[backend]; !ok {
				continue
			}
			if !f.isCapabilityAvailable("runtime." + backend) {
				continue
			}
			return backend, nil
		}
	}

	return "", fmt.Errorf("no required backend available from: %v", required)
}

func (f *Factory) expandRuntimeRequirement(raw string) []string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "linux":
		if !f.isCapabilityAvailable("runtime.linux") {
			return nil
		}
		return []string{"firecracker", "lxc"}
	case "firecracker", "lxc", "local":
		return []string{strings.ToLower(strings.TrimSpace(raw))}
	default:
		return nil
	}
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

func (f *Factory) DriverForBackend(backend string) (Driver, bool) {
	d, ok := f.drivers[backend]
	return d, ok
}

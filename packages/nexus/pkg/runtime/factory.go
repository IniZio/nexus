package runtime

import (
	"fmt"
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

	for _, backend := range required {
		if _, ok := f.drivers[backend]; !ok {
			continue
		}
		if !f.isCapabilityAvailable("runtime." + backend) {
			continue
		}
		return backend, nil
	}

	return "", fmt.Errorf("no required backend available from: %v", required)
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

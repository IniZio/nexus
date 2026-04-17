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

func (f *Factory) SelectDriver(requiredBackends []string, requiredCapabilities []string) (Driver, error) {
	for _, backend := range requiredBackends {
		driver, ok := f.drivers[backend]
		if ok {
			return driver, nil
		}
	}
	return nil, fmt.Errorf("no required backend available from: %v", requiredBackends)
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

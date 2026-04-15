package config

import "fmt"

type WorkspaceConfig struct {
	Schema           string                    `json:"$schema,omitempty"`
	Version          int                       `json:"version,omitempty"`
	InternalFeatures WorkspaceInternalFeatures `json:"internalFeatures,omitempty"`
}

type WorkspaceInternalFeatures struct {
	LocalDriver bool `json:"localDriver,omitempty"`
}

type DoctorCommandCheck struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	TimeoutMs int      `json:"timeoutMs,omitempty"`
	Retries   int      `json:"retries,omitempty"`
	Required  bool     `json:"required,omitempty"`
}

type DoctorConfig struct {
	RequiredHostPorts []int                `json:"requiredHostPorts,omitempty"`
	Probes            []DoctorCommandProbe `json:"probes,omitempty"`
	Tests             []DoctorCommandCheck `json:"tests,omitempty"`
}

type DoctorCommandProbe struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	TimeoutMs int      `json:"timeoutMs,omitempty"`
	Retries   int      `json:"retries,omitempty"`
	Required  bool     `json:"required,omitempty"`
}

func (c WorkspaceConfig) ValidateBasic() error {
	if c.Version != 0 && c.Version < 1 {
		return fmt.Errorf("version must be >= 1")
	}
	return nil
}

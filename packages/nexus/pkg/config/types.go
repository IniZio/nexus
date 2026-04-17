package config

import "fmt"

type WorkspaceConfig struct {
	Schema           string                    `json:"$schema,omitempty"`
	Version          int                       `json:"version,omitempty"`
	Isolation        WorkspaceIsolation        `json:"isolation,omitempty"`
	InternalFeatures WorkspaceInternalFeatures `json:"internalFeatures,omitempty"`
}

type WorkspaceIsolation struct {
	Level string              `json:"level,omitempty"`
	VM    WorkspaceVMSettings `json:"vm,omitempty"`
}

type WorkspaceVMSettings struct {
	Mode string `json:"mode,omitempty"`
}

type WorkspaceInternalFeatures struct {
	ProcessSandbox bool `json:"processSandbox,omitempty"`
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
	switch c.Isolation.Level {
	case "", "vm", "process":
	default:
		return fmt.Errorf("isolation.level must be one of vm or process")
	}
	switch c.Isolation.VM.Mode {
	case "", "dedicated":
	default:
		return fmt.Errorf("isolation.vm.mode must be dedicated")
	}
	return nil
}

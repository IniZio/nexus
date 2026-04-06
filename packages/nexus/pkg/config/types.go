package config

import "fmt"

type WorkspaceConfig struct {
	Schema       string                 `json:"$schema,omitempty"`
	Version      int                    `json:"version"`
	Readiness    ReadinessConfig        `json:"readiness,omitempty"`
	Services     ServicesConfig         `json:"services,omitempty"`
	Spotlight    SpotlightConfig        `json:"spotlight,omitempty"`
	Auth         AuthConfig             `json:"auth,omitempty"`
	Lifecycle    LifecycleCompatV1      `json:"lifecycle,omitempty"`
	Doctor       DoctorConfig           `json:"doctor,omitempty"`
	Runtime      RuntimeConfig          `json:"runtime,omitempty"`
	Capabilities CapabilityRequirements `json:"capabilities,omitempty"`
}

type RuntimeConfig struct {
	Required  []string `json:"required,omitempty"`
	Selection string   `json:"selection,omitempty"`
}

type CapabilityRequirements struct {
	Required []string `json:"required,omitempty"`
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

type ReadinessConfig struct {
	Profiles map[string][]ReadinessCheck `json:"profiles,omitempty"`
}

type ReadinessCheck struct {
	Name          string   `json:"name"`
	Type          string   `json:"type,omitempty"`
	Command       string   `json:"command,omitempty"`
	Args          []string `json:"args,omitempty"`
	ServiceName   string   `json:"serviceName,omitempty"`
	ExpectRunning *bool    `json:"expectRunning,omitempty"`
}

type ServicesConfig struct {
	Defaults ServiceDefaults `json:"defaults,omitempty"`
}

type ServiceDefaults struct {
	StopTimeoutMs  int  `json:"stopTimeoutMs,omitempty"`
	AutoRestart    bool `json:"autoRestart,omitempty"`
	MaxRestarts    int  `json:"maxRestarts,omitempty"`
	RestartDelayMs int  `json:"restartDelayMs,omitempty"`
}

type SpotlightConfig struct {
	Defaults []SpotlightDefault `json:"defaults,omitempty"`
}

type SpotlightDefault struct {
	Service    string `json:"service"`
	RemotePort int    `json:"remotePort"`
	LocalPort  int    `json:"localPort"`
	Host       string `json:"host,omitempty"`
}

type AuthConfig struct {
	Defaults AuthDefaults `json:"defaults,omitempty"`
}

type AuthDefaults struct {
	AuthProfiles      []string `json:"authProfiles,omitempty"`
	SSHAgentForward   *bool    `json:"sshAgentForward,omitempty"`
	GitCredentialMode string   `json:"gitCredentialMode,omitempty"`
}

type LifecycleCompatV1 struct {
	OnSetup    []string `json:"onSetup,omitempty"`
	OnStart    []string `json:"onStart,omitempty"`
	OnTeardown []string `json:"onTeardown,omitempty"`
}

func (c WorkspaceConfig) ValidateBasic() error {
	if c.Version < 1 {
		return fmt.Errorf("version must be >= 1")
	}

	for name, checks := range c.Readiness.Profiles {
		if name == "" {
			return fmt.Errorf("readiness profile name cannot be empty")
		}
		for _, check := range checks {
			if check.Name == "" {
				return fmt.Errorf("readiness check name cannot be empty")
			}
		}
	}

	if c.Services.Defaults.StopTimeoutMs < 0 {
		return fmt.Errorf("services.defaults.stopTimeoutMs must be >= 0")
	}
	if c.Services.Defaults.MaxRestarts < 0 {
		return fmt.Errorf("services.defaults.maxRestarts must be >= 0")
	}
	if c.Services.Defaults.RestartDelayMs < 0 {
		return fmt.Errorf("services.defaults.restartDelayMs must be >= 0")
	}

	for _, p := range c.Doctor.RequiredHostPorts {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("doctor.requiredHostPorts values must be in range 1-65535")
		}
	}
	for _, probe := range c.Doctor.Probes {
		if probe.Name == "" {
			return fmt.Errorf("doctor.probes[].name cannot be empty")
		}
		if probe.Command == "" {
			return fmt.Errorf("doctor.probes[].command cannot be empty")
		}
		if probe.TimeoutMs < 0 {
			return fmt.Errorf("doctor.probes[].timeoutMs must be >= 0")
		}
		if probe.Retries < 0 {
			return fmt.Errorf("doctor.probes[].retries must be >= 0")
		}
	}

	for _, allowed := range c.Runtime.Required {
		switch allowed {
		case "linux", "local", "firecracker", "vm", "lxc":
			// Allow legacy aliases for backward compatibility; runtime selection
			// logic normalizes these values.
		default:
			return fmt.Errorf("runtime.required values must include supported runtimes (linux or local)")
		}
	}
	if len(c.Runtime.Required) == 0 {
		return fmt.Errorf("runtime.required must be present and non-empty")
	}
	if c.Runtime.Selection != "" && c.Runtime.Selection != "prefer-first" {
		return fmt.Errorf("runtime.selection must be prefer-first when set")
	}

	for _, test := range c.Doctor.Tests {
		if test.Name == "" {
			return fmt.Errorf("doctor.tests[].name cannot be empty")
		}
		if test.Command == "" {
			return fmt.Errorf("doctor.tests[].command cannot be empty")
		}
		if test.TimeoutMs < 0 {
			return fmt.Errorf("doctor.tests[].timeoutMs must be >= 0")
		}
		if test.Retries < 0 {
			return fmt.Errorf("doctor.tests[].retries must be >= 0")
		}
	}

	return nil
}

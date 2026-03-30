package config

import "fmt"

type WorkspaceConfig struct {
	Schema    string            `json:"$schema,omitempty"`
	Version   int               `json:"version"`
	Readiness ReadinessConfig   `json:"readiness,omitempty"`
	Services  ServicesConfig    `json:"services,omitempty"`
	Spotlight SpotlightConfig   `json:"spotlight,omitempty"`
	Auth      AuthConfig        `json:"auth,omitempty"`
	Lifecycle LifecycleCompatV1 `json:"lifecycle,omitempty"`
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

	return nil
}

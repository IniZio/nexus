package backend

import (
	"fmt"

	"github.com/docker/docker/client"
	"github.com/nexus/nexus/packages/nexusd/internal/config"
	"github.com/nexus/nexus/packages/nexusd/internal/daytona"
	"github.com/nexus/nexus/packages/nexusd/internal/docker"
	"github.com/nexus/nexus/packages/nexusd/internal/interfaces"
	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type Manager struct {
	config         *config.Config
	dockerBackend  interfaces.Backend
	daytonaBackend interfaces.Backend
}

func NewManager(cfg *config.Config) (*Manager, error) {
	m := &Manager{
		config: cfg,
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation(), client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w", err)
	}

	dockerBE := docker.NewDockerBackend(dockerClient, cfg.Workspace.StoragePath)
	m.dockerBackend = dockerBE

	if cfg.Backends.Daytona.Enabled {
		apiKey, err := daytona.LoadAPIKey()
		if err != nil {
			return nil, fmt.Errorf("Daytona backend enabled but %w", err)
		}

		daytonaBE, err := daytona.NewBackend(
			cfg.Backends.Daytona.APIURL,
			apiKey,
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("creating Daytona backend: %w", err)
		}
		m.daytonaBackend = daytonaBE
	}

	return m, nil
}

func (m *Manager) GetBackend(backendType types.BackendType) (interfaces.Backend, error) {
	switch backendType {
	case types.BackendDocker:
		return m.dockerBackend, nil
	case types.BackendDaytona:
		if m.daytonaBackend == nil {
			return nil, fmt.Errorf("Daytona backend not initialized. Enable in config and set DAYTONA_API_KEY")
		}
		return m.daytonaBackend, nil
	default:
		return nil, fmt.Errorf("unknown backend type: %w", backendType)
	}
}

func (m *Manager) ResolveBackend(preferred string) (interfaces.Backend, types.BackendType, error) {
	if preferred != "" {
		backendType := types.BackendTypeFromString(preferred)
		backend, err := m.GetBackend(backendType)
		if err != nil {
			return nil, 0, err
		}
		return backend, backendType, nil
	}

	backendType := m.config.Workspace.DefaultBackend
	if backendType == types.BackendUnknown {
		backendType = types.BackendDocker
	}
	backend, err := m.GetBackend(backendType)
	if err != nil {
		return nil, 0, err
	}
	return backend, backendType, nil
}

func (m *Manager) GetDefaultBackend() types.BackendType {
	backendType := m.config.Workspace.DefaultBackend
	if backendType == types.BackendUnknown {
		return types.BackendDocker
	}
	return backendType
}

func (m *Manager) GetDockerBackend() interfaces.Backend {
	return m.dockerBackend
}

func (m *Manager) GetDaytonaBackend() (interfaces.Backend, bool) {
	if m.daytonaBackend == nil {
		return nil, false
	}
	return m.daytonaBackend, true
}

func ResolveBackendFromRequest(req *types.CreateWorkspaceRequest, m *Manager) (interfaces.Backend, types.BackendType, error) {
	if req.Backend != types.BackendUnknown && req.Backend != types.BackendDocker {
		be, err := m.GetBackend(req.Backend)
		if err != nil {
			return nil, 0, err
		}
		return be, req.Backend, nil
	}
	return m.ResolveBackend("")
}

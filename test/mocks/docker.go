package mocks

import (
	"context"
	"io"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

type MockDockerClient struct {
	Containers    map[string]*types.Container
	Images        map[string]bool
	Networks      map[string]bool
	mu            sync.RWMutex
	CreateCalls   []CreateCall
	StartCalls    []string
	StopCalls     []string
	RemoveCalls   []string
	ExecCalls     []ExecCall
}

type CreateCall struct {
	Name  string
	Config *container.Config
}

type ExecCall struct {
	Container string
	Cmd       []string
}

func NewMockDockerClient() *MockDockerClient {
	return &MockDockerClient{
		Containers: make(map[string]*types.Container),
		Images:     make(map[string]bool),
		Networks:   make(map[string]bool),
	}
}

func (m *MockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *string, containerName string) (container.CreateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.CreateCalls = append(m.CreateCalls, CreateCall{
		Name:  containerName,
		Config: config,
	})
	
	return container.CreateResponse{
		ID: "mock-container-" + containerName,
	}, nil
}

func (m *MockDockerClient) ContainerStart(ctx context.Context, container string, options types.ContainerStartOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.StartCalls = append(m.StartCalls, container)
	return nil
}

func (m *MockDockerClient) ContainerStop(ctx context.Context, container string, options types.ContainerStopOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.StopCalls = append(m.StopCalls, container)
	return nil
}

func (m *MockDockerClient) ContainerRemove(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.RemoveCalls = append(m.RemoveCalls, container)
	delete(m.Containers, container)
	return nil
}

func (m *MockDockerClient) ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	result := make([]types.Container, 0, len(m.Containers))
	for _, c := range m.Containers {
		result = append(result, *c)
	}
	return result, nil
}

func (m *MockDockerClient) ContainerInspect(ctx context.Context, container string) (types.ContainerJSON, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	exists := m.Containers[container]
	if !exists {
		return types.ContainerJSON{}, nil
	}
	
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			ID:   container,
			Name: "/" + container,
			State: &types.ContainerState{
				Status: "running",
			},
		},
	}, nil
}

func (m *MockDockerClient) ContainerExecCreate(ctx context.Context, container string, config types.ExecConfig) (types.IDResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.ExecCalls = append(m.ExecCalls, ExecCall{
		Container: container,
		Cmd:       config.Cmd,
	})
	
	return types.IDResponse{
		ID: "mock-exec-" + container,
	}, nil
}

func (m *MockDockerClient) ContainerExecStart(ctx context.Context, execID string, config types.ExecStartCheck) error {
	return nil
}

func (m *MockDockerClient) ContainerLogs(ctx context.Context, container string, options types.ContainerLogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(io.StringReader("mock logs")), nil
}

func (m *MockDockerClient) ImagePull(ctx context.Context, ref string, options types.ImagePullOptions) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.Images[ref] = true
	return io.NopCloser(io.StringReader("pulling")), nil
}

func (m *MockDockerClient) NetworkList(ctx context.Context, options types.NetworkListOptions) ([]types.NetworkResource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	result := make([]types.NetworkResource, 0, len(m.Networks))
	for name := range m.Networks {
		result = append(result, types.NetworkResource{
			Name: name,
		})
	}
	return result, nil
}

func (m *MockDockerClient) Ping(ctx context.Context) (types.Ping, error) {
	return types.Ping{
		APIVersion: "1.41",
	}, nil
}

func (m *MockDockerClient) Close() error {
	return nil
}

func (m *MockDockerClient) AddContainer(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.Containers[name] = &types.Container{
		ID:    name,
		Names: []string{"/" + name},
		State: "running",
	}
}

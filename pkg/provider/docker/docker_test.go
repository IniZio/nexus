package docker

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/provider"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConn implements net.Conn for testing.
type mockConn struct {
	reader *bytes.Reader
	closed bool
}

func (m *mockConn) Read(p []byte) (n int, err error) {
	return m.reader.Read(p)
}

func (m *mockConn) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func (m *mockConn) Close() error {
	m.closed = true
	return nil
}

func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

// MockDockerClient is a configurable mock for testing.
type MockDockerClient struct {
	ImagePullFn           func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	ContainerCreateFn     func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStartFn      func(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStopFn       func(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemoveFn     func(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerExecCreateFn func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error)
	ContainerExecAttachFn func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerListFn       func(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ContainerInspectFn    func(ctx context.Context, containerID string) (types.ContainerJSON, error)
}

func (m *MockDockerClient) ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
	if m.ImagePullFn != nil {
		return m.ImagePullFn(ctx, refStr, options)
	}
	return io.NopCloser(bytes.NewReader([]byte{})), nil
}

func (m *MockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	if m.ContainerCreateFn != nil {
		return m.ContainerCreateFn(ctx, config, hostConfig, networkingConfig, platform, containerName)
	}
	return container.CreateResponse{ID: "mock-container-id"}, nil
}

func (m *MockDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	if m.ContainerStartFn != nil {
		return m.ContainerStartFn(ctx, containerID, options)
	}
	return nil
}

func (m *MockDockerClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	if m.ContainerStopFn != nil {
		return m.ContainerStopFn(ctx, containerID, options)
	}
	return nil
}

func (m *MockDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	if m.ContainerRemoveFn != nil {
		return m.ContainerRemoveFn(ctx, containerID, options)
	}
	return nil
}

func (m *MockDockerClient) ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
	if m.ContainerExecCreateFn != nil {
		return m.ContainerExecCreateFn(ctx, containerID, config)
	}
	return types.IDResponse{ID: "mock-exec-id"}, nil
}

func (m *MockDockerClient) ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
	if m.ContainerExecAttachFn != nil {
		return m.ContainerExecAttachFn(ctx, execID, config)
	}
	return types.HijackedResponse{
		Conn:   &mockConn{reader: bytes.NewReader([]byte{})},
		Reader: bufio.NewReader(bytes.NewReader([]byte{})),
	}, nil
}

func (m *MockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
	if m.ContainerListFn != nil {
		return m.ContainerListFn(ctx, options)
	}
	return []types.Container{}, nil
}

func (m *MockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	if m.ContainerInspectFn != nil {
		return m.ContainerInspectFn(ctx, containerID)
	}
	return types.ContainerJSON{}, nil
}

// TestNewDockerProviderWithClient verifies the factory function.
func TestNewDockerProviderWithClient(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)
	assert.NotNil(t, p)
	assert.Equal(t, "docker", p.Name())
}

// TestDockerProvider_Name tests the Name method.
func TestDockerProvider_Name(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)
	assert.Equal(t, "docker", p.Name())
}

// TestDockerProvider_Create_Success tests successful container creation.
func TestDockerProvider_Create_Success(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ImagePullFn = func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte{})), nil
	}

	mock.ContainerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
		return container.CreateResponse{ID: "container-123"}, nil
	}

	cfg := &config.Config{
		Services: map[string]config.Service{},
		Docker: struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports,omitempty"`
			DinD  bool     `yaml:"dind,omitempty"`
		}{
			Image: "ubuntu:22.04",
		},
	}

	p := NewDockerProviderWithClient(mock)
	session, err := p.Create(context.Background(), "session-123", "/tmp/workspace", cfg)

	require.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, "container-123", session.ID)
	assert.Equal(t, "docker", session.Provider)
	assert.Equal(t, "created", session.Status)
	assert.Contains(t, session.Labels, "nexus.session.id")
}

// TestDockerProvider_Create_WithServices tests container creation with services.
func TestDockerProvider_Create_WithServices(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ImagePullFn = func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte{})), nil
	}

	mock.ContainerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
		found := false
		for _, env := range config.Env {
			if env == "NEXUS_SERVICE_DB_URL=localhost:5432" {
				found = true
				break
			}
		}
		assert.True(t, found, "service environment variable should be set")
		return container.CreateResponse{ID: "container-services"}, nil
	}

	cfg := &config.Config{
		Services: map[string]config.Service{
			"db":    {Port: 5432},
			"redis": {Port: 6379},
		},
		Docker: struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports,omitempty"`
			DinD  bool     `yaml:"dind,omitempty"`
		}{
			Image: "ubuntu:22.04",
		},
	}

	p := NewDockerProviderWithClient(mock)
	session, err := p.Create(context.Background(), "session-services", "/tmp/workspace", cfg)

	require.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, "container-services", session.ID)
}

// TestDockerProvider_Create_WithDefaultImage tests default image selection.
func TestDockerProvider_Create_WithDefaultImage(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ImagePullFn = func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
		assert.Equal(t, "ubuntu:22.04", refStr)
		return io.NopCloser(bytes.NewReader([]byte{})), nil
	}

	mock.ContainerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
		assert.Equal(t, "ubuntu:22.04", config.Image)
		return container.CreateResponse{ID: "container-default"}, nil
	}

	cfg := &config.Config{
		Services: map[string]config.Service{},
		Docker: struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports,omitempty"`
			DinD  bool     `yaml:"dind,omitempty"`
		}{
			Image: "",
		},
	}

	p := NewDockerProviderWithClient(mock)
	session, err := p.Create(context.Background(), "session-default", "/tmp/workspace", cfg)

	require.NoError(t, err)
	assert.NotNil(t, session)
}

// TestDockerProvider_Create_WithDinD tests DinD configuration.
func TestDockerProvider_Create_WithDinD(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ImagePullFn = func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte{})), nil
	}

	mock.ContainerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
		// Verify Docker socket mount is present
		found := false
		for _, m := range hostConfig.Mounts {
			if m.Target == "/var/run/docker.sock" {
				found = true
				break
			}
		}
		assert.True(t, found, "Docker socket mount should be present when DinD is enabled")
		assert.True(t, hostConfig.Privileged, "container should be privileged when DinD is enabled")
		return container.CreateResponse{ID: "container-dind"}, nil
	}

	cfg := &config.Config{
		Services: map[string]config.Service{},
		Docker: struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports,omitempty"`
			DinD  bool     `yaml:"dind,omitempty"`
		}{
			Image: "ubuntu:22.04",
			DinD:  true,
		},
	}

	p := NewDockerProviderWithClient(mock)
	session, err := p.Create(context.Background(), "session-dind", "/tmp/workspace", cfg)

	require.NoError(t, err)
	assert.NotNil(t, session)
}

// TestDockerProvider_Create_InvalidConfig tests error handling for invalid config.
func TestDockerProvider_Create_InvalidConfig(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)

	_, err := p.Create(context.Background(), "session-123", "/tmp/workspace", "invalid-config")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config type")
}

// TestDockerProvider_Create_ImagePullError tests error handling for image pull failures.
func TestDockerProvider_Create_ImagePullError(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ImagePullFn = func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
		return nil, assert.AnError
	}

	cfg := &config.Config{
		Services: map[string]config.Service{},
		Docker: struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports,omitempty"`
			DinD  bool     `yaml:"dind,omitempty"`
		}{
			Image: "ubuntu:22.04",
		},
	}

	p := NewDockerProviderWithClient(mock)
	_, err := p.Create(context.Background(), "session-error", "/tmp/workspace", cfg)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to pull image")
}

// TestDockerProvider_Create_ContainerCreateError tests error handling for container creation failures.
func TestDockerProvider_Create_ContainerCreateError(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ImagePullFn = func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte{})), nil
	}

	mock.ContainerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
		return container.CreateResponse{}, assert.AnError
	}

	cfg := &config.Config{
		Services: map[string]config.Service{},
		Docker: struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports,omitempty"`
			DinD  bool     `yaml:"dind,omitempty"`
		}{
			Image: "ubuntu:22.04",
		},
	}

	p := NewDockerProviderWithClient(mock)
	_, err := p.Create(context.Background(), "session-error", "/tmp/workspace", cfg)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create container")
}

// TestDockerProvider_Start_Success tests successful container start.
func TestDockerProvider_Start_Success(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)

	err := p.Start(context.Background(), "container-123")
	require.NoError(t, err)
}

// TestDockerProvider_Start_Error tests error handling for container start failures.
func TestDockerProvider_Start_Error(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerStartFn = func(ctx context.Context, containerID string, options container.StartOptions) error {
		return assert.AnError
	}

	p := NewDockerProviderWithClient(mock)
	err := p.Start(context.Background(), "container-123")

	assert.Error(t, err)
}

// TestDockerProvider_Stop_Success tests successful container stop.
func TestDockerProvider_Stop_Success(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)

	err := p.Stop(context.Background(), "container-123")
	require.NoError(t, err)
}

// TestDockerProvider_Stop_Error tests error handling for container stop failures.
func TestDockerProvider_Stop_Error(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
		return assert.AnError
	}

	p := NewDockerProviderWithClient(mock)
	err := p.Stop(context.Background(), "container-123")

	assert.Error(t, err)
}

// TestDockerProvider_Destroy_Success tests successful container destroy.
func TestDockerProvider_Destroy_Success(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)

	err := p.Destroy(context.Background(), "container-123")
	require.NoError(t, err)
}

// TestDockerProvider_Destroy_Error tests error handling for container destroy failures.
func TestDockerProvider_Destroy_Error(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
		return assert.AnError
	}

	p := NewDockerProviderWithClient(mock)
	err := p.Destroy(context.Background(), "container-123")

	assert.Error(t, err)
}

// TestDockerProvider_Exec_Success tests successful exec.
func TestDockerProvider_Exec_Success(t *testing.T) {
	mock := &MockDockerClient{}

	var capturedCmd []string
	mock.ContainerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
		capturedCmd = config.Cmd
		return types.IDResponse{ID: "exec-123"}, nil
	}

	mock.ContainerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return types.HijackedResponse{
			Conn:   &mockConn{reader: bytes.NewReader([]byte("output"))},
			Reader: bufio.NewReader(bytes.NewReader([]byte("output"))),
		}, nil
	}

	p := NewDockerProviderWithClient(mock)

	opts := provider.ExecOptions{
		Cmd:    []string{"echo", "hello"},
		Env:    []string{"VAR=value"},
		Stdout: true,
	}

	err := p.Exec(context.Background(), "container-123", opts)

	require.NoError(t, err)
	assert.Equal(t, []string{"echo", "hello"}, capturedCmd)
}

// TestDockerProvider_Exec_CreateError tests error handling for exec create failures.
func TestDockerProvider_Exec_CreateError(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
		return types.IDResponse{}, assert.AnError
	}

	p := NewDockerProviderWithClient(mock)

	opts := provider.ExecOptions{
		Cmd:    []string{"echo", "hello"},
		Stdout: true,
	}

	err := p.Exec(context.Background(), "container-123", opts)

	assert.Error(t, err)
}

// TestDockerProvider_Exec_AttachError tests error handling for exec attach failures.
func TestDockerProvider_Exec_AttachError(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
		return types.IDResponse{ID: "exec-123"}, nil
	}

	mock.ContainerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return types.HijackedResponse{}, assert.AnError
	}

	p := NewDockerProviderWithClient(mock)

	opts := provider.ExecOptions{
		Cmd:    []string{"echo", "hello"},
		Stdout: true,
	}

	err := p.Exec(context.Background(), "container-123", opts)

	assert.Error(t, err)
}

// TestDockerProvider_List_Empty tests listing with no containers.
func TestDockerProvider_List_Empty(t *testing.T) {
	mock := &MockDockerClient{}
	p := NewDockerProviderWithClient(mock)

	sessions, err := p.List(context.Background())

	require.NoError(t, err)
	assert.Empty(t, sessions)
}

// TestDockerProvider_List_WithSessions tests listing with nexus containers.
func TestDockerProvider_List_WithSessions(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerListFn = func(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
		return []types.Container{
			{
				ID:     "container-1",
				Status: "running",
				Labels: map[string]string{"nexus.session.id": "session-1"},
				Ports: []types.Port{
					{PrivatePort: 22, PublicPort: 2222},
					{PrivatePort: 5432, PublicPort: 5432},
				},
			},
			{
				ID:     "container-2",
				Status: "exited",
				Labels: map[string]string{"nexus.session.id": "session-2"},
				Ports:  []types.Port{},
			},
		}, nil
	}

	p := NewDockerProviderWithClient(mock)

	sessions, err := p.List(context.Background())

	require.NoError(t, err)
	assert.Len(t, sessions, 2)

	assert.Equal(t, "container-1", sessions[0].ID)
	assert.Equal(t, "docker", sessions[0].Provider)
	assert.Equal(t, "running", sessions[0].Status)
	assert.Equal(t, 2222, sessions[0].SSHPort)
	assert.Equal(t, 5432, sessions[0].Services["5432"])

	assert.Equal(t, "container-2", sessions[1].ID)
	assert.Equal(t, 0, sessions[1].SSHPort)
}

// TestDockerProvider_List_NonnexusContainers tests filtering non-nexus containers.
func TestDockerProvider_List_NonnexusContainers(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerListFn = func(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
		return []types.Container{
			{
				ID:     "regular-container",
				Status: "running",
				Labels: map[string]string{},
			},
			{
				ID:     "nexus-container",
				Status: "running",
				Labels: map[string]string{"nexus.session.id": "session-1"},
			},
		}, nil
	}

	p := NewDockerProviderWithClient(mock)

	sessions, err := p.List(context.Background())

	require.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, "nexus-container", sessions[0].ID)
}

// TestDockerProvider_List_Error tests error handling for list failures.
func TestDockerProvider_List_Error(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerListFn = func(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
		return nil, assert.AnError
	}

	p := NewDockerProviderWithClient(mock)

	sessions, err := p.List(context.Background())

	assert.Error(t, err)
	assert.Nil(t, sessions)
}

// TestDockerProvider_List_WithSSHPort tests SSH port detection.
func TestDockerProvider_List_WithSSHPort(t *testing.T) {
	mock := &MockDockerClient{}

	mock.ContainerListFn = func(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
		return []types.Container{
			{
				ID:     "container-ssh",
				Status: "running",
				Labels: map[string]string{"nexus.session.id": "session-ssh"},
				Ports: []types.Port{
					{PrivatePort: 22, PublicPort: 2222},
				},
			},
		}, nil
	}

	p := NewDockerProviderWithClient(mock)

	sessions, err := p.List(context.Background())

	require.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, 2222, sessions[0].SSHPort)
}

// TestConfigServiceStruct tests the Service config struct.
func TestConfigServiceStruct(t *testing.T) {
	svc := config.Service{
		Command:   "docker-compose up -d",
		Port:      5432,
		DependsOn: []string{"redis"},
		Env:       map[string]string{"KEY": "value"},
		Healthcheck: &config.Healthcheck{
			URL:      "http://localhost:5432/health",
			Interval: "10s",
			Timeout:  "5s",
			Retries:  3,
		},
	}

	assert.Equal(t, "docker-compose up -d", svc.Command)
	assert.Equal(t, 5432, svc.Port)
	assert.Contains(t, svc.DependsOn, "redis")
	assert.Contains(t, svc.Env, "KEY")
	assert.NotNil(t, svc.Healthcheck)
	assert.Equal(t, "http://localhost:5432/health", svc.Healthcheck.URL)
}

// TestSessionStruct tests the provider.Session struct.
func TestSessionStruct(t *testing.T) {
	session := &provider.Session{
		ID:       "test-container-id",
		Provider: "docker",
		Status:   "running",
		SSHPort:  2222,
		Services: map[string]int{
			"5432": 5432,
			"8080": 8080,
		},
		Labels: map[string]string{
			"nexus.session.id": "session-123",
			"project":          "myproject",
		},
	}

	assert.Equal(t, "test-container-id", session.ID)
	assert.Equal(t, "docker", session.Provider)
	assert.Equal(t, "running", session.Status)
	assert.Equal(t, 2222, session.SSHPort)
	assert.Len(t, session.Services, 2)
	assert.Len(t, session.Labels, 2)
}

// TestExecOptionsStruct tests the provider.ExecOptions struct.
func TestExecOptionsStruct(t *testing.T) {
	opts := provider.ExecOptions{
		Cmd:          []string{"/bin/bash", "-c", "echo hello"},
		Env:          []string{"VAR1=value1", "VAR2=value2"},
		Stdout:       true,
		Stderr:       true,
		StdoutWriter: nil,
		StderrWriter: nil,
	}

	assert.Len(t, opts.Cmd, 3)
	assert.Equal(t, "/bin/bash", opts.Cmd[0])
	assert.Len(t, opts.Env, 2)
	assert.True(t, opts.Stdout)
	assert.True(t, opts.Stderr)
}

// TestDockerClientInterface tests that MockDockerClient implements DockerClient.
func TestDockerClientInterface(t *testing.T) {
	var _ DockerClientInterface = &MockDockerClient{}
}

// TestProviderInterface tests that DockerProvider implements provider.Provider.
func TestProviderInterface(t *testing.T) {
	var p provider.Provider = NewDockerProviderWithClient(&MockDockerClient{})
	assert.Equal(t, "docker", p.Name())
}

// TestNatPort tests NAT port handling.
func TestNatPort(t *testing.T) {
	port := nat.Port("5432/tcp")
	assert.Equal(t, "5432/tcp", string(port))
}

// TestMountType tests mount type constants.
func TestMountType(t *testing.T) {
	assert.Equal(t, mount.Type("bind"), mount.TypeBind)
	assert.Equal(t, mount.Type("volume"), mount.TypeVolume)
}

// TestNewDockerProviderWithRealClient tests NewDockerProvider with a real Docker client.
// This test is skipped if Docker is not available.
func TestNewDockerProviderWithRealClient(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	p, err := NewDockerProvider()
	if err != nil {
		t.Skip("Docker not available")
	}

	assert.NotNil(t, p)
	assert.Equal(t, "docker", p.Name())
}

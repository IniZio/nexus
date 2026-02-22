package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
	nat "github.com/docker/go-connections/nat"
)

type DockerBackend struct {
	client           *Client
	docker           *client.Client
	portManager      *PortManager
	containerManager *ContainerManager
	stateDir         string
}

func NewDockerBackend(dockerClient *client.Client, stateDir string) *DockerBackend {
	return &DockerBackend{
		docker:           dockerClient,
		portManager:      NewPortManager(32800, 34999),
		containerManager: NewContainerManager(),
		stateDir:         stateDir,
	}
}

func (b *DockerBackend) CreateWorkspace(ctx context.Context, req *wsTypes.CreateWorkspaceRequest) (*wsTypes.Workspace, error) {
	workspace := &wsTypes.Workspace{
		ID:          fmt.Sprintf("ws-%d", time.Now().UnixNano()),
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Status:      wsTypes.StatusCreating,
		Backend:     wsTypes.BackendDocker,
		Repository: &wsTypes.Repository{
			URL: req.RepositoryURL,
		},
		Branch:  req.Branch,
		Config:  req.Config,
		Labels:  req.Labels,
	}

	if workspace.Config == nil {
		workspace.Config = &wsTypes.WorkspaceConfig{}
	}

	image := workspace.Config.Image
	if image == "" {
		image = "ubuntu:22.04"
	}

	if err := b.pullImage(ctx, image); err != nil {
		return nil, fmt.Errorf("pulling image: %w", err)
	}

	sshBinds, sshEnv := GetSSHAgentMounts()

	configEnv := make(map[string]string)
	if workspace.Config != nil && workspace.Config.Env != nil {
		configEnv = workspace.Config.Env
	}

	volumes := []VolumeMount{}
	for _, bind := range sshBinds {
		parts := strings.Split(bind, ":")
		if len(parts) >= 2 {
			volumes = append(volumes, VolumeMount{
				Source:   parts[0],
				Target:   parts[1],
				ReadOnly: true,
			})
		}
	}

	containerID, err := b.createContainer(ctx, image, &ContainerConfig{
		Image:      image,
		Env:        mergeEnv(configEnv, sshEnv),
		WorkingDir: "/workspace",
		AutoRemove: false,
		Volumes:    volumes,
	})
	if err != nil {
		return nil, fmt.Errorf("creating container: %w", err)
	}

	b.containerManager.mu.Lock()
	b.containerManager.containers[containerID] = &ContainerInfo{
		ID:     containerID,
		Image:  image,
		Status: "created",
		State: ContainerState{
			Status:  "created",
			Running: false,
		},
	}
	b.containerManager.mu.Unlock()

	if err := b.startContainer(ctx, containerID); err != nil {
		return nil, fmt.Errorf("starting container: %w", err)
	}

	b.containerManager.mu.Lock()
	if info, ok := b.containerManager.containers[containerID]; ok {
		info.Status = "running"
		info.State.Status = "running"
		info.State.Running = true
	}
	b.containerManager.mu.Unlock()

	workspace.Status = wsTypes.StatusRunning

	return workspace, nil
}

func (b *DockerBackend) StartWorkspace(ctx context.Context, id string) (*wsTypes.Operation, error) {
	if err := b.containerManager.Start(ctx, id); err != nil {
		return nil, err
	}

	return &wsTypes.Operation{
		ID:        fmt.Sprintf("op-%d", time.Now().UnixNano()),
		Status:    "running",
		CreatedAt: time.Now(),
	}, nil
}

func (b *DockerBackend) StopWorkspace(ctx context.Context, id string, timeout int32) (*wsTypes.Operation, error) {
	timeoutDuration := time.Duration(timeout) * time.Second
	if timeoutDuration == 0 {
		timeoutDuration = 30 * time.Second
	}

	if err := b.containerManager.Stop(ctx, id, timeoutDuration); err != nil {
		return nil, err
	}

	return &wsTypes.Operation{
		ID:         fmt.Sprintf("op-%d", time.Now().UnixNano()),
		Status:     "stopped",
		CreatedAt:  time.Now(),
		CompletedAt: time.Now(),
	}, nil
}

func (b *DockerBackend) DeleteWorkspace(ctx context.Context, id string) error {
	if err := b.containerManager.Stop(ctx, id, 10*time.Second); err != nil {
		fmt.Printf("warning: stopping container: %v\n", err)
	}

	if err := b.containerManager.Remove(ctx, id, true); err != nil {
		return fmt.Errorf("removing container: %w", err)
	}

	return nil
}

func (b *DockerBackend) GetWorkspaceStatus(ctx context.Context, id string) (wsTypes.WorkspaceStatus, error) {
	info, err := b.containerManager.Inspect(ctx, id)
	if err != nil {
		return wsTypes.StatusError, err
	}

	if info == nil {
		return wsTypes.StatusStopped, nil
	}

	switch info.State.Status {
	case "running":
		return wsTypes.StatusRunning, nil
	case "exited", "dead":
		return wsTypes.StatusStopped, nil
	case "paused":
		return wsTypes.StatusSleeping, nil
	default:
		return wsTypes.StatusCreating, nil
	}
}

func (b *DockerBackend) GetResourceStats(ctx context.Context, id string) (*wsTypes.ResourceStats, error) {
	info, err := b.containerManager.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}

	if info == nil {
		return &wsTypes.ResourceStats{
			WorkspaceID: id,
			Timestamp:   time.Now(),
		}, nil
	}

	stats := &wsTypes.ResourceStats{
		WorkspaceID: id,
		Timestamp:   time.Now(),
	}

	if info.State.Running {
		stats.CPUUsagePercent = 0
		stats.MemoryUsedBytes = 0
	}

	return stats, nil
}

func (b *DockerBackend) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	b.containerManager.mu.RLock()
	info, ok := b.containerManager.containers[id]
	b.containerManager.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("container %s not found", id)
	}

	_, reader, err := b.execInContainer(ctx, info.ID, cmd)
	if err != nil {
		return "", err
	}

	if reader == nil {
		return "", nil
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	return string(output), nil
}

func (b *DockerBackend) GetLogs(ctx context.Context, id string, tail int) (string, error) {
	return b.containerManager.Logs(ctx, id, tail)
}

func (b *DockerBackend) CopyFiles(ctx context.Context, id string, src io.Reader, dst string) error {
	return b.copyToContainer(ctx, id, src, dst)
}

func (b *DockerBackend) pullImage(ctx context.Context, image string) error {
	pullOptions := types.ImagePullOptions{}
	reader, err := b.docker.ImagePull(ctx, image, pullOptions)
	if err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}
	defer reader.Close()

	dec := json.NewDecoder(reader)
	for {
		var event struct {
			Status string `json:"status"`
		}
		if err := dec.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decoding pull event: %w", err)
		}
	}

	return nil
}

func (b *DockerBackend) createContainer(ctx context.Context, image string, config *ContainerConfig) (string, error) {
	env := []string{}
	for k, v := range config.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	hostConfig := &container.HostConfig{
		AutoRemove: config.AutoRemove,
	}

	if len(config.Ports) > 0 {
		portBindings := []nat.PortBinding{}
		for _, p := range config.Ports {
			hostPort := p.HostPort
			if hostPort == 0 {
				allocatedPort, err := b.portManager.Allocate()
				if err != nil {
					return "", err
				}
				hostPort = allocatedPort
			}
			portBindings = append(portBindings, nat.PortBinding{
				HostPort: fmt.Sprintf("%d", hostPort),
			})
		}
		hostConfig.PortBindings = nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", config.Ports[0].ContainerPort)): portBindings,
		}
	}

	if len(config.Volumes) > 0 {
		binds := []string{}
		mounts := []mount.Mount{}
		for _, v := range config.Volumes {
			if v.Type == "bind" {
				binds = append(binds, fmt.Sprintf("%s:%s:%t", v.Source, v.Target, v.ReadOnly))
			} else {
				mounts = append(mounts, mount.Mount{
					Type:   mount.Type(v.Type),
					Source: v.Source,
					Target: v.Target,
				})
			}
		}
		hostConfig.Binds = binds
		hostConfig.Mounts = mounts
	}

	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{},
	}

	resp, err := b.docker.ContainerCreate(ctx, &container.Config{
		Image:        image,
		Env:          env,
		WorkingDir:   config.WorkingDir,
		Entrypoint:   config.Entrypoint,
		Cmd:          config.Cmd,
		ExposedPorts: map[nat.Port]struct{}{},
	}, hostConfig, networkingConfig, nil, "")
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}

	return resp.ID, nil
}

func (b *DockerBackend) startContainer(ctx context.Context, id string) error {
	return b.docker.ContainerStart(ctx, id, types.ContainerStartOptions{})
}

func (b *DockerBackend) stopContainer(ctx context.Context, id string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	return b.docker.ContainerStop(ctx, id, container.StopOptions{
		Timeout: &timeoutSec,
	})
}

func (b *DockerBackend) removeContainer(ctx context.Context, id string, force bool) error {
	return b.docker.ContainerRemove(ctx, id, types.ContainerRemoveOptions{
		Force: force,
	})
}

func (b *DockerBackend) inspectContainer(ctx context.Context, id string) (types.ContainerJSON, error) {
	return b.docker.ContainerInspect(ctx, id)
}

func (b *DockerBackend) execInContainer(ctx context.Context, id string, cmd []string) (int, io.Reader, error) {
	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	resp, err := b.docker.ContainerExecCreate(ctx, id, execConfig)
	if err != nil {
		return 0, nil, err
	}

	execStartCheck := types.ExecStartCheck{}
	conn, err := b.docker.ContainerExecAttach(ctx, resp.ID, execStartCheck)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()

	var outBuf bytes.Buffer
	if _, err := io.Copy(&outBuf, conn.Reader); err != nil {
		return 0, nil, err
	}

	info, err := b.docker.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return 0, nil, err
	}

	return info.ExitCode, &outBuf, nil
}

func (b *DockerBackend) getContainerLogs(ctx context.Context, id string, tail int) (io.Reader, error) {
	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tail),
	}

	return b.docker.ContainerLogs(ctx, id, options)
}

func (b *DockerBackend) copyToContainer(ctx context.Context, id string, src io.Reader, dst string) error {
	execCmd := []string{"sh", "-c", fmt.Sprintf("tar -xf - -C %s", dst)}
	_, err := b.execInContainerWithStdin(ctx, id, execCmd, src)
	return err
}

func (b *DockerBackend) execInContainerWithStdin(ctx context.Context, id string, cmd []string, stdin io.Reader) (int, error) {
	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  true,
	}

	resp, err := b.docker.ContainerExecCreate(ctx, id, execConfig)
	if err != nil {
		return 0, err
	}

	conn, err := b.docker.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{
		Detach: false,
		Tty:    false,
	})
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if stdin != nil {
		if _, err := io.Copy(conn.Conn, stdin); err != nil {
			return 0, err
		}
		conn.CloseWrite()
	}

	var outBuf bytes.Buffer
	if _, err := io.Copy(&outBuf, conn.Reader); err != nil {
		return 0, err
	}

	info, err := b.docker.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return 0, err
	}

	return info.ExitCode, nil
}

func (b *DockerBackend) AllocatePort() (int32, error) {
	return b.portManager.Allocate()
}

func (b *DockerBackend) ReleasePort(port int32) error {
	return b.portManager.Release(port)
}

func (b *DockerBackend) GetPortManager() *PortManager {
	return b.portManager
}

func mergeEnv(configEnv map[string]string, sshEnv []string) []string {
	var env []string
	for k, v := range configEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	env = append(env, sshEnv...)
	return env
}

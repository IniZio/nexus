package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
	nat "github.com/docker/go-connections/nat"
	"nexus/pkg/sync"
)

type DockerBackend struct {
	client            *Client
	docker            *client.Client
	portManager       *PortManager
	containerManager  *ContainerManager
	stateDir          string
	syncManager       *sync.Manager
	worktreePathFunc func(workspaceID string) (string, error)
}

func NewDockerBackend(dockerClient *client.Client, stateDir string) *DockerBackend {
	syncConfig := &sync.Config{
		Mode:    "two-way-safe",
		Exclude: []string{"node_modules", ".git", "*.log"},
	}
	syncStore := sync.NewFileStateStore(stateDir)
	syncManager := sync.NewManager(syncConfig, syncStore)

	return &DockerBackend{
		docker:           dockerClient,
		portManager:      NewPortManager(32800, 34999),
		containerManager: NewContainerManager(),
		stateDir:         stateDir,
		syncManager:      syncManager,
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

	sshVolumes, sshEnv := GetSSHAgentMounts()

	configEnv := make(map[string]string)
	if workspace.Config != nil && workspace.Config.Env != nil {
		configEnv = workspace.Config.Env
	}

	volumes := append([]VolumeMount{}, sshVolumes...)

	workingDir := "/workspace"
	if req.WorktreePath != "" {
		volumes = append(volumes, VolumeMount{
			Type:     "bind",
			Source:   req.WorktreePath,
			Target:   "/workspace",
			ReadOnly: false,
		})
		workspace.Repository.LocalPath = req.WorktreePath
	}

	containerID, err := b.createContainer(ctx, image, &ContainerConfig{
		Name:      workspace.ID,
		Image:      image,
		Env:        mergeEnv(configEnv, sshEnv),
		WorkingDir: workingDir,
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

	if req.WorktreePath != "" && b.syncManager != nil {
		if _, err := b.syncManager.StartSync(ctx, workspace.ID, req.WorktreePath, "/workspace"); err != nil {
			fmt.Printf("Warning: failed to start sync: %v\n", err)
		} else {
			fmt.Printf("Started sync for workspace %s\n", workspace.ID)
		}
	}

	return workspace, nil
}

func (b *DockerBackend) CreateWorkspaceWithBridge(ctx context.Context, req *wsTypes.CreateWorkspaceRequest, bridgeSocket string) (*wsTypes.Workspace, error) {
	wsID := req.ID
	if wsID == "" {
		wsID = fmt.Sprintf("ws-%d", time.Now().UnixNano())
	}

	workspace := &wsTypes.Workspace{
		ID:          wsID,
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

	sshVolumes, sshEnv := GetSSHAgentMounts()
	bridgeVolumes, bridgeEnv := GetBridgeSocketMount(bridgeSocket)

	configEnv := make(map[string]string)
	if workspace.Config != nil && workspace.Config.Env != nil {
		configEnv = workspace.Config.Env
	}

	volumes := append([]VolumeMount{}, sshVolumes...)
	volumes = append(volumes, bridgeVolumes...)

	workingDir := "/workspace"
	if req.WorktreePath != "" {
		volumes = append(volumes, VolumeMount{
			Type:     "bind",
			Source:   req.WorktreePath,
			Target:   "/workspace",
			ReadOnly: false,
		})
		workspace.Repository.LocalPath = req.WorktreePath
	}

	allEnv := sshEnv
	allEnv = append(allEnv, bridgeEnv...)

	containerID, err := b.createContainer(ctx, image, &ContainerConfig{
		Name:      workspace.ID,
		Image:     image,
		Env:       mergeEnv(configEnv, allEnv),
		WorkingDir: workingDir,
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

	if req.WorktreePath != "" && b.syncManager != nil {
		if _, err := b.syncManager.StartSync(ctx, workspace.ID, req.WorktreePath, "/workspace"); err != nil {
			fmt.Printf("Warning: failed to start sync: %v\n", err)
		} else {
			fmt.Printf("Started sync for workspace %s\n", workspace.ID)
		}
	}

	return workspace, nil
}

func (b *DockerBackend) StartWorkspace(ctx context.Context, id string) (*wsTypes.Operation, error) {
	if err := b.containerManager.Start(ctx, id); err != nil {
		return nil, err
	}

	if b.syncManager != nil {
		if err := b.syncManager.ResumeSync(ctx, id); err != nil {
			fmt.Printf("Warning: failed to resume sync: %v\n", err)
		} else {
			fmt.Printf("Resumed sync for workspace %s\n", id)
		}
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

	if b.syncManager != nil {
		if err := b.syncManager.PauseSync(ctx, id); err != nil {
			fmt.Printf("Warning: failed to pause sync: %v\n", err)
		} else {
			fmt.Printf("Paused sync for workspace %s\n", id)
		}
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
	if b.syncManager != nil {
		if err := b.syncManager.StopSync(ctx, id); err != nil {
			fmt.Printf("Warning: stopping sync: %v\n", err)
		} else {
			fmt.Printf("Stopped sync for workspace %s\n", id)
		}
	}

	if err := b.containerManager.Stop(ctx, id, 10*time.Second); err != nil {
		fmt.Printf("warning: stopping container: %v\n", err)
	}

	if err := b.containerManager.Remove(ctx, id, true); err != nil {
		return fmt.Errorf("removing container: %w", err)
	}

	return nil
}

func (b *DockerBackend) GetWorkspaceStatus(ctx context.Context, id string) (wsTypes.WorkspaceStatus, error) {
	containers, err := b.docker.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		return wsTypes.StatusError, fmt.Errorf("listing containers: %w", err)
	}

	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+id || name == id {
				switch c.State {
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
		}
	}

	return wsTypes.StatusStopped, nil
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
	containers, err := b.docker.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		return "", fmt.Errorf("listing containers: %w", err)
	}

	var containerID string
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+id || name == id {
				containerID = c.ID
				break
			}
		}
		if containerID != "" {
			break
		}
	}

	if containerID == "" {
		return "", fmt.Errorf("container %s not found", id)
	}

	_, reader, err := b.execInContainer(ctx, containerID, cmd)
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

func (b *DockerBackend) pullImage(ctx context.Context, img string) error {
	pullOpts := image.PullOptions{}
	reader, err := b.docker.ImagePull(ctx, img, pullOpts)
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
	env := config.Env

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
				mode := "rw"
				if v.ReadOnly {
					mode = "ro"
				}
				binds = append(binds, fmt.Sprintf("%s:%s:%s", v.Source, v.Target, mode))
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
		Cmd:          defaultCmd(config.Cmd),
		ExposedPorts: map[nat.Port]struct{}{},
	}, hostConfig, networkingConfig, nil, config.Name)
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}

	return resp.ID, nil
}

func (b *DockerBackend) startContainer(ctx context.Context, id string) error {
	return b.docker.ContainerStart(ctx, id, container.StartOptions{})
}

func (b *DockerBackend) stopContainer(ctx context.Context, id string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	return b.docker.ContainerStop(ctx, id, container.StopOptions{
		Timeout: &timeoutSec,
	})
}

func (b *DockerBackend) removeContainer(ctx context.Context, id string, force bool) error {
	return b.docker.ContainerRemove(ctx, id, container.RemoveOptions{
		Force: force,
	})
}

func (b *DockerBackend) inspectContainer(ctx context.Context, id string) (types.ContainerJSON, error) {
	return b.docker.ContainerInspect(ctx, id)
}

func (b *DockerBackend) execInContainer(ctx context.Context, id string, cmd []string) (int, io.Reader, error) {
	execConfig := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Detach:       false,
		Tty:          false,
	}

	resp, err := b.docker.ContainerExecCreate(ctx, id, execConfig)
	if err != nil {
		return 0, nil, fmt.Errorf("creating exec: %w", err)
	}

	execStartCheck := container.ExecStartOptions{
		Detach: false,
		Tty:    false,
	}
	conn, err := b.docker.ContainerExecAttach(ctx, resp.ID, execStartCheck)
	if err != nil {
		return 0, nil, fmt.Errorf("attaching to exec: %w", err)
	}
	defer conn.Close()

	var outBuf bytes.Buffer
	_, err = io.Copy(&outBuf, conn.Reader)
	if err != nil {
		return 0, nil, fmt.Errorf("reading exec output: %w", err)
	}

	info, err := b.docker.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return 0, nil, fmt.Errorf("inspecting exec: %w", err)
	}

	output := decodeDockerStream(outBuf.Bytes())
	return info.ExitCode, bytes.NewReader([]byte(output)), nil
}

func decodeDockerStream(data []byte) string {
	if len(data) < 8 {
		return string(data)
	}

	var result []byte
	i := 0
	for i < len(data) {
		if i+8 > len(data) {
			result = append(result, data[i:]...)
			break
		}
		streamType := data[i]
		size := int(data[i+4])<<24 | int(data[i+5])<<16 | int(data[i+6])<<8 | int(data[i+7])
		if i+8+size > len(data) {
			result = append(result, data[i+8:]...)
			break
		}
		if streamType == 1 || streamType == 2 {
			result = append(result, data[i+8:i+8+size]...)
		}
		i += 8 + size
	}
	return string(result)
}

func (b *DockerBackend) getContainerLogs(ctx context.Context, id string, tail int) (io.Reader, error) {
	options := container.LogsOptions{
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
	execConfig := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  true,
	}

	resp, err := b.docker.ContainerExecCreate(ctx, id, execConfig)
	if err != nil {
		return 0, err
	}

	conn, err := b.docker.ContainerExecAttach(ctx, resp.ID, container.ExecStartOptions{
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

func GetBridgeSocketMount(bridgeSocket string) ([]VolumeMount, []string) {
	if bridgeSocket == "" {
		return nil, nil
	}

	volumes := []VolumeMount{
		{Type: "bind", Source: bridgeSocket, Target: "/ssh-agent", ReadOnly: true},
	}
	env := []string{"SSH_AUTH_SOCK=/ssh-agent"}

	return volumes, env
}

func defaultCmd(cmd []string) []string {
	if len(cmd) > 0 {
		return cmd
	}
	return []string{"sleep", "infinity"}
}

func (b *DockerBackend) SetWorktreePathFunc(f func(workspaceID string) (string, error)) {
	b.worktreePathFunc = f
}

func (b *DockerBackend) StartSync(ctx context.Context, workspaceID string) (string, error) {
	if b.syncManager == nil {
		return "", fmt.Errorf("sync not initialized")
	}

	worktreePath := ""
	if b.worktreePathFunc != nil {
		var err error
		worktreePath, err = b.worktreePathFunc(workspaceID)
		if err != nil {
			return "", fmt.Errorf("getting worktree path: %w", err)
		}
	}

	if worktreePath == "" {
		return "", fmt.Errorf("no worktree path configured for workspace %s", workspaceID)
	}

	sessionID, err := b.syncManager.StartSync(ctx, workspaceID, worktreePath, "/workspace")
	if err != nil {
		return "", fmt.Errorf("starting sync: %w", err)
	}

	return sessionID, nil
}

func (b *DockerBackend) PauseSync(ctx context.Context, workspaceID string) error {
	if b.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return b.syncManager.PauseSync(ctx, workspaceID)
}

func (b *DockerBackend) ResumeSync(ctx context.Context, workspaceID string) error {
	if b.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return b.syncManager.ResumeSync(ctx, workspaceID)
}

func (b *DockerBackend) StopSync(ctx context.Context, workspaceID string) error {
	if b.syncManager == nil {
		return nil
	}
	return b.syncManager.StopSync(ctx, workspaceID)
}

func (b *DockerBackend) GetSyncStatus(ctx context.Context, workspaceID string) (*wsTypes.SyncStatus, error) {
	if b.syncManager == nil {
		return nil, fmt.Errorf("sync not initialized")
	}

	status, err := b.syncManager.GetSyncStatus(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	return &wsTypes.SyncStatus{
		State:     status.State,
		LastSync:  status.LastSync,
		Conflicts: convertConflicts(status.Conflicts),
	}, nil
}

func (b *DockerBackend) FlushSync(ctx context.Context, workspaceID string) error {
	if b.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return b.syncManager.FlushSync(ctx, workspaceID)
}

func convertConflicts(conflicts []sync.Conflict) []wsTypes.Conflict {
	result := make([]wsTypes.Conflict, len(conflicts))
	for i, c := range conflicts {
		result[i] = wsTypes.Conflict{
			Path:         c.Path,
			AlphaContent: c.AlphaContent,
			BetaContent:  c.BetaContent,
		}
	}
	return result
}

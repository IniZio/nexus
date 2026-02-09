package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/provider"
	"github.com/nexus/nexus/pkg/transport"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerClientInterface defines the methods needed by DockerProvider
type DockerClientInterface interface {
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
}

// Ensure client.Client implements DockerClientInterface at compile time
var _ DockerClientInterface = (*client.Client)(nil)

type DockerProvider struct {
	cli DockerClientInterface
	transport.Manager
	remote string
}

func NewDockerProvider() (*DockerProvider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerProvider{
		cli:     cli,
		Manager: *transport.NewManager(),
	}, nil
}

// NewDockerProviderWithClient creates a DockerProvider with a custom client (useful for testing)
func NewDockerProviderWithClient(cli DockerClientInterface) *DockerProvider {
	return &DockerProvider{cli: cli}
}

func (p *DockerProvider) Name() string {
	return "docker"
}

func (p *DockerProvider) Create(ctx context.Context, sessionID string, workspacePath string, rawConfig interface{}) (*provider.Session, error) {
	cfg, ok := rawConfig.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("invalid config type")
	}

	if cfg.Remote.Node != "" {
		p.remote = fmt.Sprintf("%s@%s", cfg.Remote.User, cfg.Remote.Node)
		if cfg.Remote.Port > 0 && cfg.Remote.Port != 22 {
			p.remote = fmt.Sprintf("%s -p %d", p.remote, cfg.Remote.Port)
		}

		target := cfg.Remote.Node
		if cfg.Remote.Port > 0 && cfg.Remote.Port != 22 {
			target = fmt.Sprintf("%s:%d", cfg.Remote.Node, cfg.Remote.Port)
		} else {
			target = fmt.Sprintf("%s:22", cfg.Remote.Node)
		}

		sshKeyPath := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")

		sshConfig := transport.CreateDefaultSSHConfig(
			target,
			cfg.Remote.User,
			sshKeyPath,
		)
		if err := p.RegisterConfig("remote-docker", sshConfig); err != nil {
			return nil, fmt.Errorf("failed to register SSH transport config: %w", err)
		}

		return p.createRemote(ctx, sessionID, workspacePath, cfg)
	}

	return p.createLocal(ctx, sessionID, workspacePath, cfg)
}

func (p *DockerProvider) Start(ctx context.Context, sessionID string) error {
	if p.remote != "" {
		t, err := p.CreateTransport("remote-docker")
		if err != nil {
			return fmt.Errorf("failed to create SSH transport: %w", err)
		}

		err = t.Connect(ctx, p.remote)
		if err != nil {
			return fmt.Errorf("failed to connect via transport: %w", err)
		}
		defer t.Disconnect(ctx)

		result, err := t.Execute(ctx, &transport.Command{
			Cmd:           []string{"docker", "start", sessionID},
			CaptureOutput: true,
		})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("docker start failed: %s", result.Output)
		}
		return nil
	}

	return p.cli.ContainerStart(ctx, sessionID, container.StartOptions{})
}

func (p *DockerProvider) GetPortMappings(ctx context.Context, sessionID string) (map[string]int, error) {
	if p.remote != "" {
		return nil, fmt.Errorf("port mapping query not supported for remote Docker")
	}

	containerInfo, err := p.cli.ContainerInspect(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	portMappings := make(map[string]int)
	for containerPort, bindings := range containerInfo.NetworkSettings.Ports {
		if len(bindings) > 0 && bindings[0].HostPort != "" {
			portStr := string(containerPort)
			portStr = strings.TrimSuffix(portStr, "/tcp")

			var hostPort int
			fmt.Sscanf(bindings[0].HostPort, "%d", &hostPort)
			portMappings[portStr] = hostPort
		}
	}

	return portMappings, nil
}

func (p *DockerProvider) Stop(ctx context.Context, sessionID string) error {
	if p.remote != "" {
		t, err := p.CreateTransport("remote-docker")
		if err != nil {
			return fmt.Errorf("failed to create SSH transport: %w", err)
		}

		err = t.Connect(ctx, p.remote)
		if err != nil {
			return fmt.Errorf("failed to connect via transport: %w", err)
		}
		defer t.Disconnect(ctx)

		result, err := t.Execute(ctx, &transport.Command{
			Cmd:           []string{"docker", "stop", sessionID},
			CaptureOutput: true,
		})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("docker stop failed: %s", result.Output)
		}
		return nil
	}

	return p.cli.ContainerStop(ctx, sessionID, container.StopOptions{})
}

func (p *DockerProvider) Destroy(ctx context.Context, sessionID string) error {
	if p.remote != "" {
		t, err := p.CreateTransport("remote-docker")
		if err != nil {
			return fmt.Errorf("failed to create SSH transport: %w", err)
		}

		err = t.Connect(ctx, p.remote)
		if err != nil {
			return fmt.Errorf("failed to connect via transport: %w", err)
		}
		defer t.Disconnect(ctx)

		result, err := t.Execute(ctx, &transport.Command{
			Cmd:           []string{"docker", "rm", "-f", sessionID},
			CaptureOutput: true,
		})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("docker rm failed: %s", result.Output)
		}
		return nil
	}

	return p.cli.ContainerRemove(ctx, sessionID, container.RemoveOptions{Force: true})
}

func (p *DockerProvider) Exec(ctx context.Context, sessionID string, opts provider.ExecOptions) error {
	if p.remote != "" {
		dockerCmd := append([]string{"docker", "exec", sessionID}, opts.Cmd...)

		t, err := p.CreateTransport("remote-docker")
		if err != nil {
			return fmt.Errorf("failed to create SSH transport: %w", err)
		}

		err = t.Connect(ctx, p.remote)
		if err != nil {
			return fmt.Errorf("failed to connect via transport: %w", err)
		}
		defer t.Disconnect(ctx)

		envMap := make(map[string]string)
		for _, env := range opts.Env {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		result, err := t.Execute(ctx, &transport.Command{
			Cmd:           dockerCmd,
			Env:           envMap,
			WorkingDir:    "/workspace",
			CaptureOutput: false,
			Stdout:        opts.StdoutWriter,
			Stderr:        opts.StderrWriter,
		})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("docker exec failed with exit code %d", result.ExitCode)
		}
		return nil
	}

	execConfig := container.ExecOptions{
		Cmd:          opts.Cmd,
		Env:          opts.Env,
		WorkingDir:   "/workspace",
		AttachStdout: opts.Stdout,
		AttachStderr: opts.Stderr,
	}

	idResp, err := p.cli.ContainerExecCreate(ctx, sessionID, execConfig)
	if err != nil {
		return err
	}

	resp, err := p.cli.ContainerExecAttach(ctx, idResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()

	if opts.Stdout {
		_, _ = io.Copy(os.Stdout, resp.Reader)
	}

	return nil
}

func (p *DockerProvider) List(ctx context.Context) ([]provider.Session, error) {
	if p.remote != "" {
		return p.listRemote(ctx)
	}
	return p.listLocal(ctx)
}

func (p *DockerProvider) createLocal(ctx context.Context, sessionID string, workspacePath string, cfg *config.Config) (*provider.Session, error) {
	imgName := cfg.Docker.Image
	if imgName == "" {
		imgName = "ubuntu:22.04"
	}

	reader, err := p.cli.ImagePull(ctx, imgName, image.PullOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}
	_, _ = io.Copy(io.Discard, reader)
	reader.Close()

	exposedPorts := map[nat.Port]struct{}{"22/tcp": {}}
	portBindings := map[nat.Port][]nat.PortBinding{
		"22/tcp": {{HostIP: "0.0.0.0", HostPort: "0"}},
	}

	// Expose service ports with dynamic allocation
	for _, svc := range cfg.Services {
		if svc.Port > 0 {
			pStr := fmt.Sprintf("%d/tcp", svc.Port)
			exposedPorts[nat.Port(pStr)] = struct{}{}
			portBindings[nat.Port(pStr)] = []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: "0"}, // Auto-assign
			}
		}
	}

	// Initially set internal URLs only - external ports will be injected after container starts
	env := []string{}
	for name, svc := range cfg.Services {
		if svc.Port > 0 {
			internalServiceURL := fmt.Sprintf("localhost:%d", svc.Port)
			env = append(env, fmt.Sprintf("NEXUS_SERVICE_%s_URL=%s", strings.ToUpper(name), internalServiceURL))
		}
	}

	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: workspacePath,
			Target: "/workspace",
		},
	}

	if cfg.Docker.DinD {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: "/var/run/docker.sock",
			Target: "/var/run/docker.sock",
		})
	}

	resp, err := p.cli.ContainerCreate(ctx, &container.Config{
		Image:      imgName,
		Tty:        true,
		WorkingDir: "/workspace",
		Labels: map[string]string{
			"nexus.session.id": sessionID,
		},
		Cmd:          []string{"/bin/bash"},
		Env:          env,
		ExposedPorts: exposedPorts,
	}, &container.HostConfig{
		Mounts:       mounts,
		PortBindings: portBindings,
		Privileged:   cfg.Docker.DinD,
	}, nil, nil, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	return &provider.Session{
		ID:       resp.ID,
		Provider: p.Name(),
		Status:   "created",
		Labels: map[string]string{
			"nexus.session.id": sessionID,
		},
	}, nil
}

func (p *DockerProvider) createRemote(ctx context.Context, sessionID string, workspacePath string, cfg *config.Config) (*provider.Session, error) {
	imgName := cfg.Docker.Image
	if imgName == "" {
		imgName = "ubuntu:22.04"
	}

	exposedPorts := []string{}
	portBindings := []string{}

	env := []string{}
	for name, svc := range cfg.Services {
		if svc.Port > 0 {
			exposedPorts = append(exposedPorts, fmt.Sprintf("--expose=%d", svc.Port))
			portBindings = append(portBindings, fmt.Sprintf("-p %d:%d", svc.Port, svc.Port))
			url := fmt.Sprintf("http://localhost:%d", svc.Port)
			env = append(env, fmt.Sprintf("-e loom_SERVICE_%s_URL=%s", strings.ToUpper(name), url))
		}
	}

	mountOpt := fmt.Sprintf("-v %s:/workspace", workspacePath)

	dockerCmd := []string{
		"docker", "run", "-d",
		"--name", sessionID,
		"-p", "22",
		mountOpt,
	}
	dockerCmd = append(dockerCmd, exposedPorts...)
	dockerCmd = append(dockerCmd, env...)
	dockerCmd = append(dockerCmd, imgName, "/bin/bash")

	t, err := p.CreateTransport("remote-docker")
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH transport: %w", err)
	}

	err = t.Connect(ctx, p.remote)
	if err != nil {
		return nil, fmt.Errorf("failed to connect via transport: %w", err)
	}
	defer t.Disconnect(ctx)

	result, err := t.Execute(ctx, &transport.Command{
		Cmd:           dockerCmd,
		CaptureOutput: true,
	})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("docker run failed: %s", result.Output)
	}

	containerID := strings.TrimSpace(result.Output)

	return &provider.Session{
		ID:       containerID,
		Provider: p.Name(),
		Status:   "created",
		Labels: map[string]string{
			"nexus.session.id": sessionID,
		},
	}, nil
}

func (p *DockerProvider) listLocal(ctx context.Context) ([]provider.Session, error) {
	containers, err := p.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	var sessions []provider.Session
	for _, c := range containers {
		if id, ok := c.Labels["nexus.session.id"]; ok {
			var sshPort int
			services := make(map[string]int)
			for _, port := range c.Ports {
				if port.PrivatePort == 22 {
					sshPort = int(port.PublicPort)
				} else {
					pName := fmt.Sprintf("%d", port.PrivatePort)
					services[pName] = int(port.PublicPort)
				}
			}
			sessions = append(sessions, provider.Session{
				ID:       c.ID,
				Provider: p.Name(),
				Status:   c.Status,
				SSHPort:  sshPort,
				Services: services,
				Labels:   map[string]string{"nexus.session.id": id},
			})
		}
	}
	return sessions, nil
}

func (p *DockerProvider) listRemote(ctx context.Context) ([]provider.Session, error) {
	t, err := p.CreateTransport("remote-docker")
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH transport: %w", err)
	}

	err = t.Connect(ctx, p.remote)
	if err != nil {
		return nil, fmt.Errorf("failed to connect via transport: %w", err)
	}
	defer t.Disconnect(ctx)

	result, err := t.Execute(ctx, &transport.Command{
		Cmd:           []string{"docker", "ps", "-a", "--filter", "label=nexus.session.id", "--format", "{{.ID}}\t{{.Status}}\t{{.Labels}}"},
		CaptureOutput: true,
	})
	if err != nil {
		return nil, err
	}

	var sessions []provider.Session
	lines := strings.Split(strings.TrimSpace(result.Output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 {
			containerID := parts[0]
			status := parts[1]
			labels := parts[2]

			if sessionID := p.extractSessionID(labels); sessionID != "" {
				sessions = append(sessions, provider.Session{
					ID:       containerID,
					Provider: p.Name(),
					Status:   status,
					Labels:   map[string]string{"nexus.session.id": sessionID},
				})
			}
		}
	}
	return sessions, nil
}

func (p *DockerProvider) extractSessionID(labels string) string {
	for _, part := range strings.Split(labels, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "nexus.session.id=") {
			return strings.TrimPrefix(part, "nexus.session.id=")
		}
	}
	return ""
}

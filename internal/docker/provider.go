package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/creack/pty"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"nexus/pkg/sync"
	"nexus/internal/workspace"
	"nexus/pkg/coordination"
)

var defaultServicePorts = map[string]int{
	"web":      3000,
	"api":      5000,
	"alt-web":  8080,
	"postgres": 5432,
	"redis":    6379,
	"mysql":    3306,
	"mongo":    27017,
}

type Provider struct {
	cli         *client.Client
	storage     *coordination.TaskManager
	syncManager *sync.Manager
}

// NewProvider creates a new Docker provider
func NewProvider() (*Provider, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	storage, err := coordination.NewTaskManager(".")
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("failed to create task manager: %w", err)
	}

	syncStore := sync.NewCoordinationStore(storage)
	syncManager := sync.NewManager(nil, syncStore)

	return &Provider{cli: cli, storage: storage, syncManager: syncManager}, nil
}

// NewProviderWithStorage creates a new Docker provider with custom storage
func NewProviderWithStorage(cli *client.Client, storage *coordination.TaskManager) *Provider {
	syncStore := sync.NewCoordinationStore(storage)
	syncManager := sync.NewManager(nil, syncStore)
	return &Provider{cli: cli, storage: storage, syncManager: syncManager}
}

// NewProviderWithSync creates a new Docker provider with custom storage and sync config
func NewProviderWithSync(cli *client.Client, storage *coordination.TaskManager, syncConfig *sync.Config) *Provider {
	syncStore := sync.NewCoordinationStore(storage)
	syncManager := sync.NewManager(syncConfig, syncStore)
	return &Provider{cli: cli, storage: storage, syncManager: syncManager}
}

// NewProviderWithoutStorage creates a new Docker provider without storage (for testing)
func NewProviderWithoutStorage() (*Provider, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Provider{cli: cli, storage: nil}, nil
}

// isPortAvailable checks if a host port is available
func isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// findAvailablePort finds an available port starting from the preferred port
func findAvailablePort(preferredPort int, maxAttempts int) (int, error) {
	port := preferredPort
	for i := 0; i < maxAttempts; i++ {
		if isPortAvailable(port) {
			return port, nil
		}
		port++
		if port > 65535 {
			port = 3000 // Reset to default range
		}
	}
	return 0, fmt.Errorf("no available port found after %d attempts", maxAttempts)
}

// allocateServicePorts allocates host ports for all default services
func (p *Provider) allocateServicePorts(ctx context.Context, workspaceName string) (map[string]coordination.PortMapping, error) {
	mappings := make(map[string]coordination.PortMapping)

	for serviceName, containerPort := range defaultServicePorts {
		hostPort, err := findAvailablePort(containerPort, 100)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate port for %s: %w", serviceName, err)
		}

		mapping := coordination.PortMapping{
			ServiceName:   serviceName,
			ContainerPort: containerPort,
			HostPort:      hostPort,
			Protocol:      "tcp",
		}
		mappings[serviceName] = mapping

		if p.storage != nil {
			if err := p.storage.SavePortMapping(ctx, workspaceName, mapping); err != nil {
				return nil, fmt.Errorf("failed to save port mapping for %s: %w", serviceName, err)
			}
		}
	}

	return mappings, nil
}

// GetPortMappings returns all port mappings for a workspace
func (p *Provider) GetPortMappings(ctx context.Context, workspaceName string) ([]coordination.PortMapping, error) {
	if p.storage == nil {
		return nil, fmt.Errorf("storage not initialized")
	}
	return p.storage.GetPortMappings(ctx, workspaceName)
}

// DeletePortMappings removes all port mappings for a workspace
func (p *Provider) DeletePortMappings(ctx context.Context, workspaceName string) error {
	if p.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return p.storage.DeletePortMappings(ctx, workspaceName)
}

// ListAllPorts lists all port mappings across all workspaces
func (p *Provider) ListAllPorts(ctx context.Context) (map[string][]coordination.PortMapping, error) {
	if p.storage == nil {
		return nil, fmt.Errorf("storage not initialized")
	}
	return p.storage.ListAllPortMappings(ctx)
}

// Create creates a new workspace container
func (p *Provider) Create(ctx context.Context, name string, worktreePath string) error {
	// Check if container already exists
	containers, err := p.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	for _, c := range containers {
		if c.Labels["nexus.workspace.name"] == name {
			return fmt.Errorf("workspace %s already exists", name)
		}
	}

	// Pull image
	fmt.Println("📦 Pulling Ubuntu image...")
	reader, err := p.cli.ImagePull(ctx, "ubuntu:22.04", image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	// Get SSH key path
	homeDir, _ := os.UserHomeDir()
	sshKeyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus.pub")
	sshKey, _ := os.ReadFile(sshKeyPath)

	// Create entrypoint script
	entrypoint := `#!/bin/bash
set -e

# Install SSH and essential tools
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq openssh-server sudo git curl wget vim nano > /dev/null 2>&1

# Create dev user
useradd -m -s /bin/bash dev 2>/dev/null || true
echo "dev:dev" | chpasswd
usermod -aG sudo dev

# Setup SSH
mkdir -p /var/run/sshd
mkdir -p /home/dev/.ssh
chmod 700 /home/dev/.ssh

# Add authorized key
if [ -n "$SSH_PUB_KEY" ]; then
    echo "$SSH_PUB_KEY" > /home/dev/.ssh/authorized_keys
    chmod 600 /home/dev/.ssh/authorized_keys
    chown -R dev:dev /home/dev/.ssh
fi

# Configure sudo without password
echo "dev ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/dev

# Start SSH
/usr/sbin/sshd

# Keep container running
tail -f /dev/null
`

	// Create container
	fmt.Println("🐳 Creating container...")
	resp, err := p.cli.ContainerCreate(ctx,
		&container.Config{
			Image: "ubuntu:22.04",
			Labels: map[string]string{
				"nexus.workspace.name": name,
				"nexus.workspace":      "true",
				"nexus.workspace.path": worktreePath,
			},
			Env:          []string{fmt.Sprintf("SSH_PUB_KEY=%s", string(sshKey))},
			Cmd:          []string{"bash", "-c", entrypoint},
			ExposedPorts: nat.PortSet{"22/tcp": {}},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: worktreePath,
					Target: "/workspace",
				},
			},
			PortBindings: nat.PortMap{
				"22/tcp": {{HostIP: "0.0.0.0", HostPort: "0"}},
			},
		},
		nil,
		nil,
		fmt.Sprintf("nexus-%s", name),
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Start container
	if err := p.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Wait a moment for SSH to be ready
	fmt.Println("⏳ Waiting for SSH to be ready...")
	for i := 0; i < 30; i++ {
		containerInfo, err := p.cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			continue
		}
		if containerInfo.State.Running {
			// Get port mapping
			for port, bindings := range containerInfo.NetworkSettings.Ports {
				if string(port) == "22/tcp" && len(bindings) > 0 {
					fmt.Printf("✅ Workspace %s created (SSH port: %s)\n", name, bindings[0].HostPort)
					return nil
				}
			}
		}
		fmt.Printf(".")
	}

	return fmt.Errorf("timeout waiting for workspace to be ready")
}

// CreateWithDinD creates a workspace with Docker-in-Docker support
func (p *Provider) CreateWithDinD(ctx context.Context, name string, worktreePath string) error {
	containers, err := p.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	for _, c := range containers {
		if c.Labels["nexus.workspace.name"] == name {
			return fmt.Errorf("workspace %s already exists", name)
		}
	}

	fmt.Println("📦 Pulling Ubuntu image...")
	reader, err := p.cli.ImagePull(ctx, "ubuntu:22.04", image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	homeDir, _ := os.UserHomeDir()
	sshKeyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus.pub")
	sshKey, _ := os.ReadFile(sshKeyPath)

	entrypoint := `#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq openssh-server sudo git curl wget vim nano docker.io docker-compose-plugin > /dev/null 2>&1

useradd -m -s /bin/bash dev 2>/dev/null || true
echo "dev:dev" | chpasswd
usermod -aG sudo dev
usermod -aG docker dev

mkdir -p /var/run/sshd
mkdir -p /home/dev/.ssh
chmod 700 /home/dev/.ssh

if [ -n "$SSH_PUB_KEY" ]; then
    echo "$SSH_PUB_KEY" > /home/dev/.ssh/authorized_keys
    chmod 600 /home/dev/.ssh/authorized_keys
    chown -R dev:dev /home/dev/.ssh
fi

echo "dev ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/dev

/usr/sbin/sshd

dockerd &
sleep 2

tail -f /dev/null
`

	fmt.Println("🐳 Creating DinD container...")
	resp, err := p.cli.ContainerCreate(ctx,
		&container.Config{
			Image: "ubuntu:22.04",
			Labels: map[string]string{
				"nexus.workspace.name": name,
				"nexus.workspace":      "true",
				"nexus.workspace.path": worktreePath,
				"nexus.workspace.dind": "true",
			},
			Env:          []string{fmt.Sprintf("SSH_PUB_KEY=%s", string(sshKey))},
			Cmd:          []string{"bash", "-c", entrypoint},
			ExposedPorts: nat.PortSet{"22/tcp": {}},
		},
		&container.HostConfig{
			Privileged: true,
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: worktreePath,
					Target: "/workspace",
				},
			},
			PortBindings: nat.PortMap{
				"22/tcp": {{HostIP: "0.0.0.0", HostPort: "0"}},
			},
		},
		nil,
		nil,
		fmt.Sprintf("nexus-%s", name),
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	if err := p.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	fmt.Println("⏳ Waiting for SSH and Docker to be ready...")
	for i := 0; i < 30; i++ {
		containerInfo, err := p.cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			continue
		}
		if containerInfo.State.Running {
			for port, bindings := range containerInfo.NetworkSettings.Ports {
				if string(port) == "22/tcp" && len(bindings) > 0 {
					fmt.Printf("✅ DinD workspace %s created (SSH port: %s)\n", name, bindings[0].HostPort)
					return nil
				}
			}
		}
		fmt.Printf(".")
	}

	return fmt.Errorf("timeout waiting for workspace to be ready")
}

// Start starts a workspace container
func (p *Provider) Start(ctx context.Context, name string) error {
	containerName := fmt.Sprintf("nexus-%s", name)

	// Check if running
	containerInfo, err := p.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return fmt.Errorf("workspace not found: %w", err)
	}

	if containerInfo.State.Running {
		fmt.Printf("Workspace %s is already running\n", name)
		return nil
	}

	if err := p.cli.ContainerStart(ctx, containerName, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	fmt.Printf("✅ Workspace %s started\n", name)
	return nil
}

// Stop stops a workspace container
func (p *Provider) Stop(ctx context.Context, name string) error {
	containerName := fmt.Sprintf("nexus-%s", name)

	if err := p.cli.ContainerStop(ctx, containerName, container.StopOptions{}); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	fmt.Printf("✅ Workspace %s stopped\n", name)
	return nil
}

// Destroy removes a workspace container
func (p *Provider) Destroy(ctx context.Context, name string) error {
	containerName := fmt.Sprintf("nexus-%s", name)

	// Check if container exists first (idempotent behavior)
	containerInfo, err := p.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		// Check if it's a "not found" error - if so, container doesn't exist, return nil (idempotent)
		if client.IsErrNotFound(err) {
			fmt.Printf("✅ Workspace %s already destroyed (not found)\n", name)
			return nil
		}
		return fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}

	// Stop container if running with timeout
	if containerInfo.State.Running {
		stopTimeout := 30 // seconds
		fmt.Printf("⏹️  Stopping container %s (timeout: %ds)...\n", name, stopTimeout)

		if err := p.cli.ContainerStop(ctx, containerName, container.StopOptions{
			Timeout: &stopTimeout,
		}); err != nil {
			// Even if stop fails, try to remove - force remove will handle it
			fmt.Printf("⚠️  Stop failed, attempting force remove: %v\n", err)
		}
	}

	// Remove container (force if needed)
	if err := p.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	}); err != nil {
		if client.IsErrNotFound(err) {
			// Container was already removed (e.g., force remove cleaned it up)
			fmt.Printf("✅ Workspace %s already destroyed\n", name)
			return nil
		}
		return fmt.Errorf("failed to remove container %s: %w", containerName, err)
	}

	fmt.Printf("✅ Workspace %s destroyed\n", name)
	return nil
}

// GetSSHPort returns the SSH port for a workspace
func (p *Provider) GetSSHPort(ctx context.Context, name string) (string, error) {
	containerName := fmt.Sprintf("nexus-%s", name)

	containerInfo, err := p.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("workspace not found: %w", err)
	}

	for port, bindings := range containerInfo.NetworkSettings.Ports {
		if string(port) == "22/tcp" && len(bindings) > 0 {
			return bindings[0].HostPort, nil
		}
	}

	return "", fmt.Errorf("SSH port not found")
}

// Shell opens an SSH shell to the workspace
func (p *Provider) Shell(ctx context.Context, name string) error {
	port, err := p.GetSSHPort(ctx, name)
	if err != nil {
		return err
	}

	homeDir, _ := os.UserHomeDir()
	keyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus")

	cmd := exec.Command("ssh",
		"-p", port,
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"dev@localhost",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ExecResult contains the result of a command execution
type ExecResult struct {
	ExitCode int
	Stdout   bytes.Buffer
	Stderr   bytes.Buffer
}

// Exec runs a command in the workspace via SSH
func (p *Provider) Exec(ctx context.Context, name string, command []string) error {
	port, err := p.GetSSHPort(ctx, name)
	if err != nil {
		return err
	}

	homeDir, _ := os.UserHomeDir()
	keyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus")

	args := []string{
		"-p", port,
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"dev@localhost",
	}
	args = append(args, command...)

	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ExecInteractive runs a command with PTY support for interactive sessions
func (p *Provider) ExecInteractive(ctx context.Context, name string, command []string, timeout time.Duration) (*ExecResult, error) {
	port, err := p.GetSSHPort(ctx, name)
	if err != nil {
		return nil, err
	}

	homeDir, _ := os.UserHomeDir()
	keyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus")

	args := []string{
		"-p", port,
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "RequestTTY=force",
		"dev@localhost",
	}
	args = append(args, command...)

	cmd := exec.Command("ssh", args...)

	ptyFile, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start PTY: %w", err)
	}
	defer ptyFile.Close()

	done := make(chan struct{})
	var result ExecResult

	go func() {
		defer close(done)
		io.Copy(&result.Stdout, ptyFile)
	}()

	select {
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done
		return nil, ctx.Err()
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return nil, fmt.Errorf("command timed out after %v", timeout)
	case <-done:
		result.ExitCode = 0
		return &result, nil
	}
}

// ExecWithOutput runs a command and captures the output
func (p *Provider) ExecWithOutput(ctx context.Context, name string, command []string, timeout time.Duration) (*ExecResult, error) {
	port, err := p.GetSSHPort(ctx, name)
	if err != nil {
		return nil, err
	}

	homeDir, _ := os.UserHomeDir()
	keyPath := filepath.Join(homeDir, ".ssh", "id_ed25519_nexus")

	args := []string{
		"-p", port,
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"dev@localhost",
	}
	args = append(args, command...)

	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done
		return nil, ctx.Err()
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return nil, fmt.Errorf("command timed out after %v", timeout)
	case err := <-done:
		return &ExecResult{
			ExitCode: 0,
			Stdout:   stdout,
			Stderr:   stderr,
		}, err
	}
}

// List returns all workspaces
func (p *Provider) List(ctx context.Context) ([]workspace.WorkspaceInfo, error) {
	containers, err := p.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var workspaces []workspace.WorkspaceInfo
	for _, c := range containers {
		if name, ok := c.Labels["nexus.workspace.name"]; ok {
			status := "stopped"
			if c.State == "running" {
				status = "running"
			}

			// Get SSH port
			port := ""
			containerInfo, err := p.cli.ContainerInspect(ctx, c.ID)
			if err == nil {
				for prt, bindings := range containerInfo.NetworkSettings.Ports {
					if string(prt) == "22/tcp" && len(bindings) > 0 {
						port = bindings[0].HostPort
						break
					}
				}
			}

			workspaces = append(workspaces, workspace.WorkspaceInfo{
				Name:   name,
				Status: status,
				Port:   port,
			})
		}
	}

	return workspaces, nil
}

// Close closes the Docker client
func (p *Provider) Close() error {
	return p.cli.Close()
}

// ContainerExists checks if a workspace container exists
func (p *Provider) ContainerExists(ctx context.Context, name string) (bool, error) {
	containerName := fmt.Sprintf("nexus-%s", name)
	_, err := p.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect container: %w", err)
	}
	return true, nil
}

// StartSync starts the Mutagen sync for a workspace
func (p *Provider) StartSync(ctx context.Context, workspaceName, worktreePath string) (string, error) {
	if p.syncManager == nil {
		return "", fmt.Errorf("sync not initialized")
	}

	containerPath := "/workspace"
	return p.syncManager.StartSync(ctx, workspaceName, worktreePath, containerPath)
}

// PauseSync pauses the Mutagen sync for a workspace
func (p *Provider) PauseSync(ctx context.Context, workspaceName string) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return p.syncManager.PauseSync(ctx, workspaceName)
}

// ResumeSync resumes the Mutagen sync for a workspace
func (p *Provider) ResumeSync(ctx context.Context, workspaceName string) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return p.syncManager.ResumeSync(ctx, workspaceName)
}

// StopSync stops and terminates the Mutagen sync for a workspace
func (p *Provider) StopSync(ctx context.Context, workspaceName string) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return p.syncManager.StopSync(ctx, workspaceName)
}

// GetSyncStatus gets the sync status for a workspace
func (p *Provider) GetSyncStatus(ctx context.Context, workspaceName string) (interface{}, error) {
	if p.syncManager == nil {
		return nil, fmt.Errorf("sync not initialized")
	}
	return p.syncManager.GetSyncStatus(ctx, workspaceName)
}

// FlushSync flushes the sync for a workspace
func (p *Provider) FlushSync(ctx context.Context, workspaceName string) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync not initialized")
	}
	return p.syncManager.FlushSync(ctx, workspaceName)
}

// SetSyncConfig sets the sync configuration for the provider
func (p *Provider) SetSyncConfig(config *sync.Config) {
	if p.storage != nil {
		p.syncManager = sync.NewManager(config, sync.NewCoordinationStore(p.storage))
	}
}

package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nexus/nexus/pkg/provider"
)

type WorkspaceManager struct {
	providers          map[string]provider.Provider
	workspaces         map[string]*ManagedWorkspace
	portAllocationLock sync.Mutex
	portRange          PortAllocationRange
	mu                 sync.RWMutex
}

type PortAllocationRange struct {
	SSHStart       int
	SSHEnd         int
	ServiceStart   int
	ServiceEnd     int
	allocatedPorts map[int]bool
}

type ManagedWorkspace struct {
	Command          *CreateWorkspaceCommand
	ContainerID      string
	Status           WorkspaceStatus
	StartedAt        time.Time
	Services         map[string]*ManagedService
	SSHPort          int
	ContainerIP      string
	LastStatusUpdate time.Time
	ErrorMessage     string
	mu               sync.RWMutex
}

type ManagedService struct {
	Definition   ServiceDefinition
	Status       ServiceStatus
	Port         int
	MappedPort   int
	StartedAt    time.Time
	HealthStatus string
	LastCheck    time.Time
	ErrorMessage string
}

func NewWorkspaceManager(providers map[string]provider.Provider) *WorkspaceManager {
	return &WorkspaceManager{
		providers: providers,
		workspaces: make(map[string]*ManagedWorkspace),
		portRange: PortAllocationRange{
			SSHStart:       2222,
			SSHEnd:         2299,
			ServiceStart:   23000,
			ServiceEnd:     30000,
			allocatedPorts: make(map[int]bool),
		},
	}
}

func (pm *PortAllocationRange) AllocateSSHPort() (int, error) {
	for port := pm.SSHStart; port <= pm.SSHEnd; port++ {
		if !pm.allocatedPorts[port] {
			pm.allocatedPorts[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available SSH ports in range %d-%d", pm.SSHStart, pm.SSHEnd)
}

func (pm *PortAllocationRange) AllocateServicePort() (int, error) {
	for port := pm.ServiceStart; port <= pm.ServiceEnd; port++ {
		if !pm.allocatedPorts[port] {
			pm.allocatedPorts[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available service ports in range %d-%d", pm.ServiceStart, pm.ServiceEnd)
}

func (pm *PortAllocationRange) ReleasePort(port int) {
	delete(pm.allocatedPorts, port)
}

func (wm *WorkspaceManager) CreateWorkspace(ctx context.Context, cmd *CreateWorkspaceCommand) (*WorkspaceCreateResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workspace command: %w", err)
	}

	// Allocate SSH port
	wm.portAllocationLock.Lock()
	sshPort, err := wm.portRange.AllocateSSHPort()
	wm.portAllocationLock.Unlock()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate SSH port: %w", err)
	}

	workspace := &ManagedWorkspace{
		Command:   cmd,
		Status:    WorkspaceStatusCreating,
		StartedAt: time.Now(),
		Services:  make(map[string]*ManagedService),
		SSHPort:   sshPort,
	}

	wm.mu.Lock()
	wm.workspaces[cmd.WorkspaceID] = workspace
	wm.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	providerImpl, ok := wm.providers[cmd.Provider]
	if !ok {
		result := &WorkspaceCreateResult{
			WorkspaceID: cmd.WorkspaceID,
			Status:      WorkspaceStatusError,
			Error:       fmt.Sprintf("provider %s not available", cmd.Provider),
			Timestamp:   time.Now(),
		}
		workspace.Status = WorkspaceStatusError
		workspace.ErrorMessage = result.Error
		return result, nil
	}

	// Create workspace path
	workspacePath := fmt.Sprintf("/var/lib/nexus/workspaces/%s", cmd.WorkspaceID)

	session, err := providerImpl.Create(ctx, cmd.WorkspaceID, workspacePath, nil)
	if err != nil {
		result := &WorkspaceCreateResult{
			WorkspaceID: cmd.WorkspaceID,
			Status:      WorkspaceStatusError,
			Error:       fmt.Sprintf("failed to create container: %v", err),
			Timestamp:   time.Now(),
		}
		workspace.Status = WorkspaceStatusError
		workspace.ErrorMessage = result.Error
		return result, nil
	}

	workspace.ContainerID = session.ID

	// Start container
	if err := providerImpl.Start(ctx, cmd.WorkspaceID); err != nil {
		_ = providerImpl.Destroy(ctx, cmd.WorkspaceID)
		result := &WorkspaceCreateResult{
			WorkspaceID: cmd.WorkspaceID,
			Status:      WorkspaceStatusError,
			Error:       fmt.Sprintf("failed to start container: %v", err),
			Timestamp:   time.Now(),
		}
		workspace.Status = WorkspaceStatusError
		workspace.ErrorMessage = result.Error
		return result, nil
	}

	// Configure SSH in container
	if err := wm.configureSSH(ctx, providerImpl, cmd.WorkspaceID, cmd.SSH); err != nil {
		_ = providerImpl.Stop(ctx, cmd.WorkspaceID)
		_ = providerImpl.Destroy(ctx, cmd.WorkspaceID)
		result := &WorkspaceCreateResult{
			WorkspaceID: cmd.WorkspaceID,
			Status:      WorkspaceStatusError,
			Error:       fmt.Sprintf("failed to configure SSH: %v", err),
			Timestamp:   time.Now(),
		}
		workspace.Status = WorkspaceStatusError
		workspace.ErrorMessage = result.Error
		return result, nil
	}

	workspace.Status = WorkspaceStatusRunning

	result := &WorkspaceCreateResult{
		WorkspaceID: cmd.WorkspaceID,
		ContainerID: workspace.ContainerID,
		Status:      WorkspaceStatusRunning,
		SSHPort:     sshPort,
		Services:    make(map[string]int),
		Timestamp:   time.Now(),
	}

	return result, nil
}

func (wm *WorkspaceManager) configureSSH(ctx context.Context, prov provider.Provider, workspaceID string, sshCfg SSHConfig) error {
	setupSSHScript := fmt.Sprintf(`#!/bin/bash
set -e

# Update package manager
apt-get update -qq

# Install SSH server
apt-get install -y openssh-server > /dev/null 2>&1

# Create user
useradd -m -s /bin/bash %s || true

# Setup SSH directory
mkdir -p /home/%s/.ssh
chmod 700 /home/%s/.ssh

# Add public key
echo "%s" > /home/%s/.ssh/authorized_keys
chmod 600 /home/%s/.ssh/authorized_keys
chown -R %s:%s /home/%s/.ssh

# Configure SSH daemon
mkdir -p /run/sshd
sed -i 's/#PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
sed -i 's/#PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/#PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config

# Start SSH server
service ssh start || /usr/sbin/sshd -D &
sleep 2

echo "SSH configured successfully"
`, sshCfg.User, sshCfg.User, sshCfg.User, sshCfg.PubKey, sshCfg.User, sshCfg.User, sshCfg.User, sshCfg.User, sshCfg.User)

	cmd := provider.ExecOptions{
		Cmd: []string{"/bin/bash", "-c", setupSSHScript},
	}

	if err := prov.Exec(ctx, workspaceID, cmd); err != nil {
		return fmt.Errorf("failed to configure SSH in container: %w", err)
	}

	// Verify SSH is listening
	time.Sleep(1 * time.Second)
	verifyCmd := provider.ExecOptions{
		Cmd: []string{"netstat", "-tlnp"},
	}
	if err := prov.Exec(ctx, workspaceID, verifyCmd); err != nil {
		log.Printf("Warning: Could not verify SSH is listening, but continuing: %v", err)
	}

	return nil
}

func (wm *WorkspaceManager) StartServices(ctx context.Context, workspaceID string) (*WorkspaceStatusUpdate, error) {
	wm.mu.RLock()
	workspace, exists := wm.workspaces[workspaceID]
	wm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("workspace %s not found", workspaceID)
	}

	prov, ok := wm.providers[workspace.Command.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %s not available", workspace.Command.Provider)
	}

	// Resolve service dependencies
	orderedServices, err := wm.resolveServiceDependencies(workspace.Command.Services)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve service dependencies: %w", err)
	}

	serviceStatus := make(map[string]string)
	servicePortMap := make(map[string]int)

	// Start services in dependency order
	for _, svc := range orderedServices {
		wm.portAllocationLock.Lock()
		mappedPort, err := wm.portRange.AllocateServicePort()
		wm.portAllocationLock.Unlock()

		if err != nil {
			serviceStatus[svc.Name] = "error"
			continue
		}

		managedSvc := &ManagedService{
			Definition: svc,
			Port:       svc.Port,
			MappedPort: mappedPort,
			Status:     ServiceStatusStarting,
		}

		workspace.mu.Lock()
		workspace.Services[svc.Name] = managedSvc
		workspace.mu.Unlock()

		// Start service with retries
		err = wm.startServiceWithRetry(ctx, prov, workspaceID, svc, 3, 1*time.Second)
		if err != nil {
			serviceStatus[svc.Name] = "error"
			managedSvc.Status = ServiceStatusError
			managedSvc.ErrorMessage = err.Error()
			log.Printf("Error starting service %s: %v", svc.Name, err)
			continue
		}

		managedSvc.Status = ServiceStatusRunning
		managedSvc.StartedAt = time.Now()
		serviceStatus[svc.Name] = "running"
		servicePortMap[svc.Name] = mappedPort

		// Run health check if configured
		if svc.HealthCheck != nil {
			go func(s ServiceDefinition) {
				time.Sleep(2 * time.Second)
				wm.runHealthCheck(ctx, prov, workspaceID, s)
			}(svc)
		}
	}

	workspace.mu.Lock()
	workspace.LastStatusUpdate = time.Now()
	workspace.mu.Unlock()

	return &WorkspaceStatusUpdate{
		WorkspaceID: workspaceID,
		Status:      WorkspaceStatusRunning,
		Services:    serviceStatus,
		Timestamp:   time.Now(),
	}, nil
}

func (wm *WorkspaceManager) startServiceWithRetry(ctx context.Context, prov provider.Provider, workspaceID string, svc ServiceDefinition, maxRetries int, backoff time.Duration) error {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff * time.Duration(1<<uint(attempt-1))):
			}
		}

		cmd := provider.ExecOptions{
			Cmd: []string{"/bin/bash", "-c", fmt.Sprintf("cd /workspace && %s", svc.Command)},
		}

		err := prov.Exec(ctx, workspaceID, cmd)
		if err == nil {
			return nil
		}

		lastErr = err
		log.Printf("Service %s start attempt %d failed: %v", svc.Name, attempt+1, err)
	}

	return fmt.Errorf("failed to start service %s after %d attempts: %w", svc.Name, maxRetries, lastErr)
}

func (wm *WorkspaceManager) runHealthCheck(ctx context.Context, prov provider.Provider, workspaceID string, svc ServiceDefinition) {
	wm.mu.RLock()
	workspace, exists := wm.workspaces[workspaceID]
	wm.mu.RUnlock()

	if !exists {
		return
	}

	workspace.mu.RLock()
	managedSvc, exists := workspace.Services[svc.Name]
	workspace.mu.RUnlock()

	if !exists {
		return
	}

	hc := svc.HealthCheck
	if hc == nil {
		return
	}

	timeout := 10 * time.Second
	if hc.Timeout > 0 {
		timeout = time.Duration(hc.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch hc.Type {
	case HealthCheckHTTP:
		wm.checkHTTPHealth(ctx, prov, workspaceID, svc, managedSvc)
	case HealthCheckTCP:
		wm.checkTCPHealth(ctx, prov, workspaceID, svc, managedSvc)
	case HealthCheckExec:
		wm.checkExecHealth(ctx, prov, workspaceID, svc, managedSvc)
	}

	workspace.mu.Lock()
	managedSvc.LastCheck = time.Now()
	workspace.mu.Unlock()
}

func (wm *WorkspaceManager) checkHTTPHealth(ctx context.Context, prov provider.Provider, workspaceID string, svc ServiceDefinition, managedSvc *ManagedService) {
	path := "/health"
	if svc.HealthCheck.Path != "" {
		path = svc.HealthCheck.Path
	}

	cmd := provider.ExecOptions{
		Cmd: []string{"/bin/bash", "-c", fmt.Sprintf("curl -sf http://localhost:%d%s", svc.Port, path)},
	}

	if err := prov.Exec(ctx, workspaceID, cmd); err == nil {
		managedSvc.HealthStatus = "healthy"
		managedSvc.Status = ServiceStatusRunning
	} else {
		managedSvc.HealthStatus = "unhealthy"
		managedSvc.Status = ServiceStatusUnhealthy
		managedSvc.ErrorMessage = err.Error()
	}
}

func (wm *WorkspaceManager) checkTCPHealth(ctx context.Context, prov provider.Provider, workspaceID string, svc ServiceDefinition, managedSvc *ManagedService) {
	cmd := provider.ExecOptions{
		Cmd: []string{"/bin/bash", "-c", fmt.Sprintf("nc -z localhost %d", svc.Port)},
	}

	if err := prov.Exec(ctx, workspaceID, cmd); err == nil {
		managedSvc.HealthStatus = "healthy"
		managedSvc.Status = ServiceStatusRunning
	} else {
		managedSvc.HealthStatus = "unhealthy"
		managedSvc.Status = ServiceStatusUnhealthy
	}
}

func (wm *WorkspaceManager) checkExecHealth(ctx context.Context, prov provider.Provider, workspaceID string, svc ServiceDefinition, managedSvc *ManagedService) {
	cmd := provider.ExecOptions{
		Cmd: []string{"/bin/bash", "-c", svc.HealthCheck.Command},
	}

	if err := prov.Exec(ctx, workspaceID, cmd); err == nil {
		managedSvc.HealthStatus = "healthy"
		managedSvc.Status = ServiceStatusRunning
	} else {
		managedSvc.HealthStatus = "unhealthy"
		managedSvc.Status = ServiceStatusUnhealthy
	}
}

func (wm *WorkspaceManager) resolveServiceDependencies(services []ServiceDefinition) ([]ServiceDefinition, error) {
	if len(services) == 0 {
		return services, nil
	}

	// Build dependency graph
	depGraph := make(map[string][]string)
	serviceMap := make(map[string]ServiceDefinition)

	for _, svc := range services {
		depGraph[svc.Name] = svc.DependsOn
		serviceMap[svc.Name] = svc
	}

	// Topological sort
	var result []ServiceDefinition
	visited := make(map[string]bool)
	visiting := make(map[string]bool)

	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("circular dependency detected involving service %s", name)
		}

		visiting[name] = true

		for _, dep := range depGraph[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}

		visiting[name] = false
		visited[name] = true
		result = append(result, serviceMap[name])
		return nil
	}

	for _, svc := range services {
		if err := visit(svc.Name); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (wm *WorkspaceManager) StopWorkspace(ctx context.Context, workspaceID string) error {
	wm.mu.RLock()
	workspace, exists := wm.workspaces[workspaceID]
	wm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	workspace.mu.Lock()
	workspace.Status = WorkspaceStatusStopped
	workspace.mu.Unlock()

	prov, ok := wm.providers[workspace.Command.Provider]
	if !ok {
		return fmt.Errorf("provider %s not available", workspace.Command.Provider)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := prov.Stop(ctx, workspaceID); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	return nil
}

func (wm *WorkspaceManager) DeleteWorkspace(ctx context.Context, workspaceID string) error {
	wm.mu.RLock()
	workspace, exists := wm.workspaces[workspaceID]
	wm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("workspace %s not found", workspaceID)
	}

	prov, ok := wm.providers[workspace.Command.Provider]
	if !ok {
		return fmt.Errorf("provider %s not available", workspace.Command.Provider)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := prov.Destroy(ctx, workspaceID); err != nil {
		return fmt.Errorf("failed to destroy container: %w", err)
	}

	// Release ports
	wm.portAllocationLock.Lock()
	wm.portRange.ReleasePort(workspace.SSHPort)
	for _, svc := range workspace.Services {
		wm.portRange.ReleasePort(svc.MappedPort)
	}
	wm.portAllocationLock.Unlock()

	wm.mu.Lock()
	delete(wm.workspaces, workspaceID)
	wm.mu.Unlock()

	return nil
}

func (wm *WorkspaceManager) GetWorkspaceStatus(workspaceID string) *WorkspaceStatusUpdate {
	wm.mu.RLock()
	workspace, exists := wm.workspaces[workspaceID]
	wm.mu.RUnlock()

	if !exists {
		return nil
	}

	workspace.mu.RLock()
	defer workspace.mu.RUnlock()

	serviceStatus := make(map[string]string)
	for name, svc := range workspace.Services {
		serviceStatus[name] = string(svc.Status)
	}

	msg := ""
	if workspace.Status == WorkspaceStatusError {
		msg = workspace.ErrorMessage
	}

	return &WorkspaceStatusUpdate{
		WorkspaceID: workspace.Command.WorkspaceID,
		Status:      workspace.Status,
		Message:     msg,
		Services:    serviceStatus,
		Timestamp:   time.Now(),
	}
}

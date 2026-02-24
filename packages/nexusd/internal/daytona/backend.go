package daytona

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/interfaces"
	"github.com/nexus/nexus/packages/nexusd/internal/ssh"
	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type DaytonaBackend struct {
	client *Client
	apiURL string
	apiKey string

	idMapping map[string]string
	mappingMu sync.RWMutex

	stateStore daytonaStateStore
}

type daytonaStateStore interface {
	SaveDaytonaMapping(nexusID, daytonaID string) error
	GetDaytonaMapping(nexusID string) (string, error)
	DeleteDaytonaMapping(nexusID string) error
}

var _ interfaces.Backend = (*DaytonaBackend)(nil)

func NewBackend(apiURL, apiKey string, store daytonaStateStore) (*DaytonaBackend, error) {
	client, err := NewClient(apiURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("creating Daytona client: %w", err)
	}

	backend := &DaytonaBackend{
		client:     client,
		apiURL:     apiURL,
		apiKey:     apiKey,
		idMapping:  make(map[string]string),
		stateStore: store,
	}

	return backend, nil
}

func (b *DaytonaBackend) CreateWorkspace(ctx context.Context, req *types.CreateWorkspaceRequest) (*types.Workspace, error) {
	resources := b.mapResources(req)

	createReq := CreateSandboxRequest{
		Name:             req.Name,
		Class:            resources.Class,
		AutoStopInterval: b.mapIdleTimeout(req.Config),
	}

	if req.Config != nil && req.Config.Env != nil {
		createReq.EnvVars = req.Config.Env
	}

	sandbox, err := b.client.CreateSandbox(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("creating Daytona sandbox: %w", err)
	}

	b.setDaytonaID(req.Name, sandbox.ID)

	workspace := &types.Workspace{
		ID:      req.Name,
		Name:    req.Name,
		Backend: types.BackendDaytona,
		Status:  mapSandboxState(sandbox.State),
		Config:  req.Config,
		DaytonaMetadata: &types.DaytonaMetadata{
			SandboxID:     sandbox.ID,
			SSHHost:       sandbox.SSHInfo.Host,
			SSHPort:       sandbox.SSHInfo.Port,
			SSHUsername:   sandbox.SSHInfo.Username,
			SSHPrivateKey: sandbox.SSHInfo.PrivateKey,
		},
	}

	return workspace, nil
}

func (b *DaytonaBackend) CreateWorkspaceWithBridge(ctx context.Context, req *types.CreateWorkspaceRequest, bridgeSocket string) (*types.Workspace, error) {
	return b.CreateWorkspace(ctx, req)
}

func (b *DaytonaBackend) StartWorkspace(ctx context.Context, id string) (*types.Operation, error) {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return nil, err
	}

	if err := b.client.StartSandbox(ctx, daytonaID); err != nil {
		return nil, fmt.Errorf("starting sandbox: %w", err)
	}

	return &types.Operation{
		ID:     fmt.Sprintf("daytona-%s-start", id),
		Status: "started",
	}, nil
}

func (b *DaytonaBackend) StopWorkspace(ctx context.Context, id string, timeout int32) (*types.Operation, error) {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return nil, err
	}

	if err := b.client.StopSandbox(ctx, daytonaID); err != nil {
		return nil, fmt.Errorf("stopping sandbox: %w", err)
	}

	return &types.Operation{
		ID:     fmt.Sprintf("daytona-%s-stop", id),
		Status: "stopped",
	}, nil
}

func (b *DaytonaBackend) DeleteWorkspace(ctx context.Context, id string) error {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return err
	}

	if err := b.client.DeleteSandbox(ctx, daytonaID); err != nil {
		return fmt.Errorf("deleting sandbox: %w", err)
	}

	b.removeDaytonaID(id)

	return nil
}

func (b *DaytonaBackend) GetWorkspaceStatus(ctx context.Context, id string) (types.WorkspaceStatus, error) {
	return b.GetStatus(ctx, id)
}

func (b *DaytonaBackend) GetStatus(ctx context.Context, id string) (types.WorkspaceStatus, error) {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return types.StatusUnknown, err
	}

	sandbox, err := b.client.GetSandbox(ctx, daytonaID)
	if err != nil {
		return types.StatusUnknown, fmt.Errorf("getting sandbox status: %w", err)
	}

	return mapSandboxState(sandbox.State), nil
}

func (b *DaytonaBackend) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	conn, err := b.GetSSHConnection(ctx, id)
	if err != nil {
		return "", err
	}

	return ssh.Execute(ctx, conn, cmd)
}

func (b *DaytonaBackend) ExecViaSSH(ctx context.Context, id string, cmd []string) (string, error) {
	status, err := b.GetStatus(ctx, id)
	if err != nil {
		return "", err
	}

	if status == types.StatusStopped {
		fmt.Fprintf(os.Stderr, "Workspace %s is sleeping. Starting...\n", id)
		if _, err := b.StartWorkspace(ctx, id); err != nil {
			return "", fmt.Errorf("auto-start failed: %w", err)
		}
		if err := b.waitForRunning(ctx, id, 30*time.Second); err != nil {
			return "", fmt.Errorf("waiting for workspace to start: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Workspace started. Executing command...\n")
	}

	conn, err := b.GetSSHConnection(ctx, id)
	if err != nil {
		return "", err
	}

	return ssh.Execute(ctx, conn, cmd)
}

func (b *DaytonaBackend) GetLogs(ctx context.Context, id string, tail int) (string, error) {
	return "", fmt.Errorf("GetLogs not implemented for Daytona backend")
}

func (b *DaytonaBackend) GetResourceStats(ctx context.Context, id string) (*types.ResourceStats, error) {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return nil, err
	}

	sandbox, err := b.client.GetSandbox(ctx, daytonaID)
	if err != nil {
		return nil, fmt.Errorf("getting sandbox: %w", err)
	}

	return &types.ResourceStats{
		WorkspaceID:      id,
		CPUUsagePercent:  0,
		MemoryUsedBytes:  0,
		MemoryLimitBytes: int64(sandbox.Memory) * 1024 * 1024 * 1024,
		DiskUsedBytes:    0,
	}, nil
}

func (b *DaytonaBackend) GetSSHPort(ctx context.Context, id string) (int32, error) {
	conn, err := b.GetSSHConnection(ctx, id)
	if err != nil {
		return 0, err
	}
	return conn.Port, nil
}

func (b *DaytonaBackend) Shell(ctx context.Context, id string) error {
	status, err := b.GetStatus(ctx, id)
	if err != nil {
		return err
	}

	if status == types.StatusStopped {
		fmt.Fprintf(os.Stderr, "Workspace %s is sleeping. Starting...\n", id)
		if _, err := b.StartWorkspace(ctx, id); err != nil {
			return fmt.Errorf("auto-start failed: %w", err)
		}
		if err := b.waitForRunning(ctx, id, 30*time.Second); err != nil {
			return err
		}
	}

	conn, err := b.GetSSHConnection(ctx, id)
	if err != nil {
		return err
	}

	return ssh.Shell(ctx, conn)
}

func (b *DaytonaBackend) CopyFiles(ctx context.Context, id string, src io.Reader, dst string) error {
	return fmt.Errorf("CopyFiles not implemented for Daytona backend - use Mutagen sync")
}

func (b *DaytonaBackend) PauseSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (b *DaytonaBackend) ResumeSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (b *DaytonaBackend) FlushSync(ctx context.Context, workspaceID string) error {
	return nil
}

func (b *DaytonaBackend) GetSyncStatus(ctx context.Context, workspaceID string) (*types.SyncStatus, error) {
	return &types.SyncStatus{
		State: "unknown",
	}, nil
}

func (b *DaytonaBackend) AllocatePort() (int32, error) {
	return 0, nil
}

func (b *DaytonaBackend) ReleasePort(port int32) error {
	return nil
}

func (b *DaytonaBackend) AddPortBinding(ctx context.Context, workspaceID string, containerPort, hostPort int32) error {
	return nil
}

func (b *DaytonaBackend) CommitContainer(ctx context.Context, workspaceID string, req *types.CommitContainerRequest) error {
	return fmt.Errorf("CommitContainer not supported for Daytona backend")
}

func (b *DaytonaBackend) RemoveImage(ctx context.Context, imageName string) error {
	return fmt.Errorf("RemoveImage not supported for Daytona backend")
}

func (b *DaytonaBackend) RestoreFromImage(ctx context.Context, workspaceID, imageName string) error {
	return fmt.Errorf("RestoreFromImage not supported for Daytona backend")
}

func (b *DaytonaBackend) GetSSHConnection(ctx context.Context, id string) (*types.SSHConnection, error) {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return nil, err
	}

	sandbox, err := b.client.GetSandbox(ctx, daytonaID)
	if err != nil {
		return nil, fmt.Errorf("getting sandbox info: %w", err)
	}

	if !sandbox.IsRunning() {
		return nil, fmt.Errorf("workspace is not running (state: %s)", sandbox.State)
	}

	sshAccess, err := b.client.CreateSSHAccess(ctx, daytonaID, 60)
	if err != nil {
		return nil, fmt.Errorf("creating SSH access: %w", err)
	}

	return &types.SSHConnection{
		Host:       "ssh.app.daytona.io",
		Port:       22,
		Username:   sshAccess.Token,
		PrivateKey: "",
	}, nil
}

func mapSandboxState(state string) types.WorkspaceStatus {
	switch state {
	case "creating", "pending":
		return types.StatusCreating
	case "started", "running":
		return types.StatusRunning
	case "stopped":
		return types.StatusStopped
	case "error":
		return types.StatusError
	default:
		return types.StatusUnknown
	}
}

func (b *DaytonaBackend) getDaytonaID(nexusID string) (string, error) {
	b.mappingMu.RLock()
	if id, ok := b.idMapping[nexusID]; ok {
		b.mappingMu.RUnlock()
		return id, nil
	}
	b.mappingMu.RUnlock()

	if b.stateStore != nil {
		if id, err := b.stateStore.GetDaytonaMapping(nexusID); err == nil && id != "" {
			b.setDaytonaID(nexusID, id)
			return id, nil
		}
	}

	return "", fmt.Errorf("no Daytona ID found for workspace %s", nexusID)
}

func (b *DaytonaBackend) setDaytonaID(nexusID, daytonaID string) {
	b.mappingMu.Lock()
	b.idMapping[nexusID] = daytonaID
	b.mappingMu.Unlock()

	if b.stateStore != nil {
		if err := b.stateStore.SaveDaytonaMapping(nexusID, daytonaID); err != nil {
			log.Printf("Warning: failed to save ID mapping: %v", err)
		}
	}
}

func (b *DaytonaBackend) removeDaytonaID(nexusID string) {
	b.mappingMu.Lock()
	delete(b.idMapping, nexusID)
	b.mappingMu.Unlock()

	if b.stateStore != nil {
		b.stateStore.DeleteDaytonaMapping(nexusID)
	}
}

func (b *DaytonaBackend) mapResources(req *types.CreateWorkspaceRequest) Resources {
	if req.ResourceClass != "" {
		return getResourcesForClass(req.ResourceClass)
	}
	return getResourcesForClass("standard")
}

func (b *DaytonaBackend) mapIdleTimeout(config *types.WorkspaceConfig) int {
	if config == nil || config.IdleTimeout == 0 {
		return 15
	}
	return int(config.IdleTimeout)
}

func getResourcesForClass(class string) Resources {
	switch class {
	case "small":
		return Resources{CPU: 1, Memory: 1, Disk: 3, Class: "small"}
	case "medium":
		return Resources{CPU: 2, Memory: 4, Disk: 20, Class: "medium"}
	case "large":
		return Resources{CPU: 4, Memory: 8, Disk: 40, Class: "large"}
	default:
		return Resources{CPU: 1, Memory: 1, Disk: 3, Class: "small"}
	}
}

func (b *DaytonaBackend) waitForRunning(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := b.GetStatus(ctx, id)
			if err != nil {
				return err
			}

			if status == types.StatusRunning {
				return nil
			}

			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for workspace to start")
			}
		}
	}
}

func (b *DaytonaBackend) GetWorkspaceInfo(ctx context.Context, id string) (*WorkspaceInfo, error) {
	daytonaID, err := b.getDaytonaID(id)
	if err != nil {
		return nil, err
	}

	sandbox, err := b.client.GetSandbox(ctx, daytonaID)
	if err != nil {
		return nil, err
	}

	info := &WorkspaceInfo{
		ID:               id,
		Status:           mapSandboxState(sandbox.State),
		Backend:          types.BackendDaytona,
		SandboxID:        sandbox.ID,
		Image:            sandbox.Image,
		Resources:        Resources{CPU: sandbox.CPU, Memory: sandbox.Memory, Disk: sandbox.Disk, Class: sandbox.Class},
		AutoStopInterval: sandbox.AutoStopInterval,
	}

	if sandbox.IsRunning() && sandbox.AutoStopInterval > 0 {
		info.TTL = fmt.Sprintf("%dm remaining", sandbox.AutoStopInterval)
	}

	return info, nil
}

type WorkspaceInfo struct {
	ID               string
	Status           types.WorkspaceStatus
	Backend          types.BackendType
	SandboxID        string
	Image            string
	Resources        Resources
	AutoStopInterval int
	TTL              string
}

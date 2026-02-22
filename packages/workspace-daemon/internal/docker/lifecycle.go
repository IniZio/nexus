package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

type Lifecycle struct {
	containerID  string
	hooks        map[string][]string
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	healthCheck  HealthChecker
	autoRestart  bool
	restartCount int
	maxRestarts  int
}

func NewLifecycle(containerID string) *Lifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &Lifecycle{
		containerID: containerID,
		hooks:       make(map[string][]string),
		ctx:         ctx,
		cancel:      cancel,
		healthCheck: &defaultHealthChecker{},
		autoRestart: true,
		maxRestarts: 3,
	}
}

func (l *Lifecycle) PreStart(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runHooks(ctx, "pre-start")
}

func (l *Lifecycle) PostStart(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runHooks(ctx, "post-start")
}

func (l *Lifecycle) PreStop(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runHooks(ctx, "pre-stop")
}

func (l *Lifecycle) PostStop(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runHooks(ctx, "post-stop")
}

func (l *Lifecycle) RegisterHook(phase string, command string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hooks[phase] = append(l.hooks[phase], command)
}

func (l *Lifecycle) RunHook(ctx context.Context, phase string) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.runHooks(ctx, phase)
}

func (l *Lifecycle) runHooks(ctx context.Context, phase string) error {
	hooks, ok := l.hooks[phase]
	if !ok || len(hooks) == 0 {
		return nil
	}

	for _, cmd := range hooks {
		log.Printf("Running %s hook: %s", phase, cmd)
	}
	return nil
}

func (l *Lifecycle) StartHealthMonitor(backend *DockerBackend, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-l.ctx.Done():
				return
			case <-ticker.C:
				l.checkHealth(backend)
			}
		}
	}()
}

func (l *Lifecycle) checkHealth(backend *DockerBackend) {
	ctx, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	defer cancel()

	status, err := backend.GetWorkspaceStatus(ctx, l.containerID)
	if err != nil {
		log.Printf("Health check failed for %s: %v", l.containerID, err)
		return
	}

	if status == types.StatusError {
		log.Printf("Container %s in error state", l.containerID)
		if l.autoRestart && l.restartCount < l.maxRestarts {
			l.restart(backend)
		}
	}
}

func (l *Lifecycle) restart(backend *DockerBackend) {
	l.restartCount++
	log.Printf("Attempting restart %d/%d for container %s", 
		l.restartCount, l.maxRestarts, l.containerID)

	ctx, cancel := context.WithTimeout(l.ctx, 30*time.Second)
	defer cancel()

	if _, err := backend.StopWorkspace(ctx, l.containerID, 10); err != nil {
		log.Printf("Failed to stop container %s: %v", l.containerID, err)
	}

	time.Sleep(2 * time.Second)

	if _, err := backend.StartWorkspace(ctx, l.containerID); err != nil {
		log.Printf("Failed to start container %s: %v", l.containerID, err)
	}
}

func (l *Lifecycle) Stop() {
	l.cancel()
}

func (l *Lifecycle) SetAutoRestart(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.autoRestart = enabled
}

func (l *Lifecycle) SetMaxRestarts(max int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxRestarts = max
}

func (l *Lifecycle) SetHealthChecker(checker HealthChecker) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.healthCheck = checker
}

type HealthChecker interface {
	Check(ctx context.Context, containerID string) error
}

type defaultHealthChecker struct{}

func (h *defaultHealthChecker) Check(ctx context.Context, containerID string) error {
	return nil
}

type ContainerManager struct {
	mu              sync.RWMutex
	containerTimeout time.Duration
	containers      map[string]*ContainerInfo
}

func NewContainerManager() *ContainerManager {
	return &ContainerManager{
		containerTimeout: 30 * time.Second,
		containers:      make(map[string]*ContainerInfo),
	}
}

func (m *ContainerManager) Create(ctx context.Context, image string, config *ContainerConfig) (string, error) {
	containerID := fmt.Sprintf("ws-%s", generateID())
	m.mu.Lock()
	m.containers[containerID] = &ContainerInfo{
		ID:     containerID,
		Image:  image,
		Status: "created",
		State: ContainerState{
			Status:  "created",
			Running: false,
		},
	}
	m.mu.Unlock()
	return containerID, nil
}

func (m *ContainerManager) Start(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, ok := m.containers[id]
	if !ok {
		return fmt.Errorf("container %s not found", id)
	}

	info.Status = "running"
	info.State.Status = "running"
	info.State.Running = true
	return nil
}

func (m *ContainerManager) Stop(ctx context.Context, id string, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, ok := m.containers[id]
	if !ok {
		return fmt.Errorf("container %s not found", id)
	}

	info.Status = "exited"
	info.State.Status = "exited"
	info.State.Running = false
	info.State.ExitCode = 0
	return nil
}

func (m *ContainerManager) Remove(ctx context.Context, id string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.containers[id]; !ok {
		return fmt.Errorf("container %s not found", id)
	}

	delete(m.containers, id)
	return nil
}

func (m *ContainerManager) Inspect(ctx context.Context, id string) (*ContainerInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, ok := m.containers[id]
	if !ok {
		return nil, nil
	}
	return info, nil
}

func (m *ContainerManager) Logs(ctx context.Context, id string, tail int) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, ok := m.containers[id]
	if !ok {
		return "", fmt.Errorf("container %s not found", id)
	}

	return fmt.Sprintf("[%s] Container logs for %s", time.Now().Format(time.RFC3339), info.ID), nil
}

func (m *ContainerManager) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.containers[id]
	if !ok {
		return "", fmt.Errorf("container %s not found", id)
	}

	return fmt.Sprintf("Executed: %s", strings.Join(cmd, " ")), nil
}

func (m *ContainerManager) ExecWithStdin(ctx context.Context, id string, cmd []string, stdin io.Reader) (int, error) {
	m.mu.RLock()
	_, ok := m.containers[id]
	m.mu.RUnlock()

	if !ok {
		return 1, fmt.Errorf("container %s not found", id)
	}

	return 0, nil
}

func generateID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

type ContainerConfig struct {
	Image      string
	Env        []string
	Volumes    []VolumeMount
	Ports      []PortBinding
	WorkingDir string
	Entrypoint []string
	Cmd        []string
	AutoRemove bool
}

type VolumeMount struct {
	Source      string
	Target      string
	ReadOnly    bool
}

type PortBinding struct {
	ContainerPort int32
	HostPort      int32
	Protocol      string
}

type ContainerInfo struct {
	ID      string
	Name    string
	Image   string
	Status  string
	Created time.Time
	State   ContainerState
}

type ContainerState struct {
	Status     string
	Running    bool
	Paused     bool
	Restarting bool
	OOMKilled  bool
	Dead       bool
	Pid        int
	ExitCode   int
}

type LifecycleManager struct {
	mu           sync.RWMutex
	lifecycles   map[string]*Lifecycle
	backend      *DockerBackend
	shutdownChan chan struct{}
	wg           sync.WaitGroup
}

func NewLifecycleManager(backend *DockerBackend) *LifecycleManager {
	return &LifecycleManager{
		lifecycles:   make(map[string]*Lifecycle),
		backend:      backend,
		shutdownChan: make(chan struct{}),
	}
}

func (lm *LifecycleManager) Register(containerID string) *Lifecycle {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	lifecycle := NewLifecycle(containerID)
	lm.lifecycles[containerID] = lifecycle
	return lifecycle
}

func (lm *LifecycleManager) Unregister(containerID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lifecycle, ok := lm.lifecycles[containerID]; ok {
		lifecycle.Stop()
		delete(lm.lifecycles, containerID)
	}
}

func (lm *LifecycleManager) Get(containerID string) (*Lifecycle, bool) {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	lifecycle, ok := lm.lifecycles[containerID]
	return lifecycle, ok
}

func (lm *LifecycleManager) StartAll() {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	for _, lifecycle := range lm.lifecycles {
		lifecycle.StartHealthMonitor(lm.backend, 30*time.Second)
	}
}

func (lm *LifecycleManager) StopAll() {
	close(lm.shutdownChan)
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	for _, lifecycle := range lm.lifecycles {
		lifecycle.Stop()
	}
}

func (lm *LifecycleManager) GracefulShutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		lm.StopAll()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

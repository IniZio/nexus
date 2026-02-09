package orchestration

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nexus/nexus/pkg/config"
	"github.com/nexus/nexus/pkg/provider"
)

type ServiceStatus string

const (
	ServiceStatusStopped  ServiceStatus = "stopped"
	ServiceStatusStarting ServiceStatus = "starting"
	ServiceStatusRunning  ServiceStatus = "running"
	ServiceStatusError    ServiceStatus = "error"
)

type ServiceHealth struct {
	Name      string        `json:"name"`
	Status    ServiceStatus `json:"status"`
	Healthy   bool          `json:"healthy"`
	Message   string        `json:"message,omitempty"`
	LastCheck time.Time     `json:"last_check"`
	URL       string        `json:"url,omitempty"`
}

type Orchestrator interface {
	Start(ctx context.Context, services map[string]config.Service) error
	Stop(ctx context.Context, serviceName string) error
	GetStatus(serviceName string) (*ServiceHealth, error)
	ListServices() []ServiceHealth
	StopAll(ctx context.Context) error
}

type BaseOrchestrator struct {
	provider provider.Provider
	services map[string]config.Service
	status   map[string]*ServiceHealth
	mutex    sync.RWMutex
}

func NewOrchestrator(provider provider.Provider) Orchestrator {
	return &BaseOrchestrator{
		provider: provider,
		services: make(map[string]config.Service),
		status:   make(map[string]*ServiceHealth),
	}
}

func (o *BaseOrchestrator) Start(ctx context.Context, services map[string]config.Service) error {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	o.services = services

	for name := range services {
		o.status[name] = &ServiceHealth{
			Name:    name,
			Status:  ServiceStatusStopped,
			Healthy: false,
		}
	}

	ordered := o.resolveDependencies(services)

	var wg sync.WaitGroup
	errChan := make(chan error, len(ordered))

	for _, name := range ordered {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()

			svc := o.services[name]
			log.Printf("Starting service: %s", name)

			o.mutex.Lock()
			o.status[name].Status = ServiceStatusStarting
			o.mutex.Unlock()

			if err := o.startService(ctx, name, svc); err != nil {
				o.mutex.Lock()
				o.status[name].Status = ServiceStatusError
				o.status[name].Message = err.Error()
				o.mutex.Unlock()
				errChan <- fmt.Errorf("failed to start service %s: %w", name, err)
				return
			}

			o.mutex.Lock()
			o.status[name].Status = ServiceStatusRunning
			o.status[name].Healthy = true
			o.status[name].Message = "Service started successfully"
			o.status[name].LastCheck = time.Now()
			if svc.Port > 0 {
				o.status[name].URL = fmt.Sprintf("http://localhost:%d", svc.Port)
			}
			o.mutex.Unlock()

			go o.monitorHealth(ctx, name, svc)
		}(name)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			return err
		}
	}

	log.Printf("All services started successfully")
	return nil
}

func (o *BaseOrchestrator) Stop(ctx context.Context, name string) error {
	o.mutex.Lock()
	defer o.mutex.Unlock()

	_, exists := o.services[name]
	if !exists {
		return fmt.Errorf("service %s not found", name)
	}

	log.Printf("Stopping service: %s", name)

	if o.status[name] != nil {
		o.status[name].Status = ServiceStatusStopped
		o.status[name].Healthy = false
	}

	sessionID := fmt.Sprintf("service-%s", name)
	return o.provider.Stop(ctx, sessionID)
}

func (o *BaseOrchestrator) GetStatus(serviceName string) (*ServiceHealth, error) {
	o.mutex.RLock()
	defer o.mutex.RUnlock()

	status, exists := o.status[serviceName]
	if !exists {
		return nil, fmt.Errorf("service %s not found", serviceName)
	}

	return status, nil
}

func (o *BaseOrchestrator) ListServices() []ServiceHealth {
	o.mutex.RLock()
	defer o.mutex.RUnlock()

	var services []ServiceHealth
	for _, status := range o.status {
		services = append(services, *status)
	}

	return services
}

func (o *BaseOrchestrator) StopAll(ctx context.Context) error {
	o.mutex.RLock()
	serviceNames := make([]string, 0, len(o.services))
	for name := range o.services {
		serviceNames = append(serviceNames, name)
	}
	o.mutex.RUnlock()

	var wg sync.WaitGroup
	errChan := make(chan error, len(serviceNames))

	for _, name := range serviceNames {
		wg.Add(1)
		go func(serviceName string) {
			defer wg.Done()
			if err := o.Stop(ctx, serviceName); err != nil {
				errChan <- err
			}
		}(name)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			return err
		}
	}

	return nil
}

func (o *BaseOrchestrator) resolveDependencies(services map[string]config.Service) []string {
	depGraph := make(map[string][]string)
	for name := range services {
		depGraph[name] = services[name].DependsOn
	}

	var result []string
	visited := make(map[string]bool)

	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true

		for _, dep := range depGraph[name] {
			visit(dep)
		}
		result = append(result, name)
	}

	for name := range services {
		if !visited[name] {
			visit(name)
		}
	}

	return result
}

func (o *BaseOrchestrator) startService(ctx context.Context, name string, svc config.Service) error {
	sessionID := fmt.Sprintf("service-%s", name)

	sess, err := o.provider.Create(ctx, sessionID, "/workspace", nil)
	if err != nil {
		return fmt.Errorf("failed to create session for service %s: %w", name, err)
	}

	if err := o.provider.Start(ctx, sess.ID); err != nil {
		return fmt.Errorf("failed to start session for service %s: %w", name, err)
	}

	return o.provider.Exec(ctx, sessionID, provider.ExecOptions{
		Cmd:    []string{"sh", "-c", svc.Command},
		Stdout: false,
		Stderr: false,
	})
}

func (o *BaseOrchestrator) monitorHealth(ctx context.Context, name string, svc config.Service) {
	if svc.Port == 0 {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			healthy := o.checkServiceHealth(name, svc.Port)

			o.mutex.Lock()
			o.status[name].Healthy = healthy
			o.status[name].LastCheck = time.Now()
			if !healthy {
				o.status[name].Status = ServiceStatusError
				o.status[name].Message = "Service health check failed"
			} else {
				o.status[name].Status = ServiceStatusRunning
				o.status[name].Message = "Service is healthy"
			}
			o.mutex.Unlock()
		}
	}
}

func (o *BaseOrchestrator) checkServiceHealth(serviceName string, port int) bool {
	return true
}

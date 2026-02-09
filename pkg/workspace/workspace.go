// Package workspace provides multi-workspace support for Nexus.
// It enables isolated development environments with shared coordination.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// WorkspaceState represents the current state of a workspace.
type WorkspaceState string

const (
	StatePending    WorkspaceState = "pending"
	StateCreating   WorkspaceState = "creating"
	StateRunning   WorkspaceState = "running"
	StateStopped   WorkspaceState = "stopped"
	StateArchived  WorkspaceState = "archived"
	StateFailed    WorkspaceState = "failed"
)

// ResourceType represents the type of resource.
type ResourceType string

const (
	ResourceCPU      ResourceType = "cpu"
	ResourceMemory   ResourceType = "memory"
	ResourceStorage  ResourceType = "storage"
	ResourceNetwork  ResourceType = "network"
)

// Workspace represents an isolated development environment.
type Workspace struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Project     string            `json:"project"`
	Owner       string            `json:"owner"`

	// State
	State       WorkspaceState   `json:"state"`
	StateReason string           `json:"state_reason,omitempty"`

	// Configuration
	Template   string            `json:"template,omitempty"`
	Config     *WorkspaceConfig  `json:"config"`

	// Resources
	Resources  *ResourceQuota    `json:"resources"`
	Usage      *ResourceUsage    `json:"usage"`

	// Isolation
	Namespace  string            `json:"namespace"`
	Network    string            `json:"network"`

	// Metadata
	Labels     map[string]string `json:"labels"`
	Tags       []string          `json:"tags"`
	Metadata   map[string]string `json:"metadata,omitempty"`

	// Timing
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	AccessedAt time.Time        `json:"accessed_at"`

	// Retention
	ExpiresAt  *time.Time       `json:"expires_at,omitempty"`
	AutoDelete bool              `json:"auto_delete"`
}

// WorkspaceConfig defines the configuration for a workspace.
type WorkspaceConfig struct {
	// Environment
	Environment     map[string]string `json:"environment"`

	// Volumes (mount points)
	Volumes        []VolumeMount     `json:"volumes"`

	// Ports to expose
	Ports          []PortMapping    `json:"ports"`

	// Commands
	InitCommand    []string         `json:"init_command,omitempty"`
	StartCommand   string           `json:"start_command,omitempty"`

	// Health checks
	HealthCheck    *HealthCheckConfig `json:"health_check,omitempty"`

	// Security
	Privileged     bool             `json:"privileged"`
	ReadOnlyRoot   bool             `json:"read_only_root"`

	// Extensions
	Extensions     []string         `json:"extensions,omitempty"`
}

// VolumeMount represents a volume mount in the workspace.
type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	SubPath   string `json:"sub_path,omitempty"`
	ReadOnly  bool   `json:"read_only"`
}

// PortMapping represents a port mapping for the workspace.
type PortMapping struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

// HealthCheckConfig defines health check configuration.
type HealthCheckConfig struct {
	Endpoint   string        `json:"endpoint"`
	Interval   time.Duration `json:"interval"`
	Timeout    time.Duration `json:"timeout"`
	Retries    int           `json:"retries"`
}

// ResourceQuota defines resource limits for a workspace.
type ResourceQuota struct {
	CPUCores    float64 `json:"cpu_cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	StorageBytes int64  `json:"storage_bytes"`
	GPUCount    int     `json:"gpu_count"`

	// Network limits
	BandwidthBytes int64  `json:"bandwidth_bytes"`
	MaxConnections int    `json:"max_connections"`
}

// ResourceUsage tracks current resource usage.
type ResourceUsage struct {
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryBytes int64   `json:"memory_bytes"`
	StorageBytes int64  `json:"storage_bytes"`
	BandwidthBytes int64 `json:"bandwidth_bytes"`

	UpdatedAt time.Time `json:"updated_at"`
}

// WorkspaceTemplate defines a reusable workspace template.
type WorkspaceTemplate struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`

	// Template content
	Config     *WorkspaceConfig  `json:"config"`
	Resources  *ResourceQuota    `json:"resources"`

	// Metadata
	Labels     map[string]string `json:"labels"`
	IsPublic   bool              `json:"is_public"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

// WorkspaceEvent represents an event in a workspace's lifecycle.
type WorkspaceEvent struct {
	ID          string        `json:"id"`
	WorkspaceID string        `json:"workspace_id"`
	Type        string        `json:"type"`
	Reason      string        `json:"reason"`
	Message     string        `json:"message"`
	Timestamp   time.Time     `json:"timestamp"`
}

// WorkspaceManager manages multiple workspaces.
type WorkspaceManager struct {
	mu          sync.RWMutex
	workspaces  map[string]*Workspace
	templates   map[string]*WorkspaceTemplate
	events      map[string][]WorkspaceEvent

	// Configuration
	config      *ManagerConfig

	// Callbacks
	CreateHook  func(ctx context.Context, ws *Workspace) error
	StartHook   func(ctx context.Context, ws *Workspace) error
	StopHook    func(ctx context.Context, ws *Workspace) error
	DeleteHook  func(ctx context.Context, ws *Workspace) error

	// State
	eventID     int64
}

// ManagerConfig holds configuration for the workspace manager.
type ManagerConfig struct {
	// Default quotas
	DefaultCPU    float64 `json:"default_cpu"`
	DefaultMemory int64   `json:"default_memory"`
	DefaultStorage int64  `json:"default_storage"`

	// Limits
	MaxWorkspaces int     `json:"max_workspaces"`
	MaxTemplates  int     `json:"max_templates"`

	// Retention
	DefaultTTL    time.Duration `json:"default_ttl"`
	MaxTTL        time.Duration `json:"max_ttl"`

	// Cleanup
	AutoCleanup   bool          `json:"auto_cleanup"`
	CleanupInterval time.Duration `json:"cleanup_interval"`
}

// NewWorkspaceManager creates a new workspace manager.
func NewWorkspaceManager(config *ManagerConfig) *WorkspaceManager {
	if config == nil {
		config = &ManagerConfig{
			DefaultCPU:    2.0,
			DefaultMemory: 4 * 1024 * 1024 * 1024, // 4GB
			DefaultStorage: 20 * 1024 * 1024 * 1024, // 20GB
			MaxWorkspaces: 50,
			MaxTemplates:  20,
			DefaultTTL:    7 * 24 * time.Hour,
			MaxTTL:        30 * 24 * time.Hour,
			AutoCleanup:   true,
		}
	}

	return &WorkspaceManager{
		workspaces: make(map[string]*Workspace),
		templates:  make(map[string]*WorkspaceTemplate),
		events:     make(map[string][]WorkspaceEvent),
		config:     config,
	}
}

// CreateWorkspace creates a new workspace.
func (m *WorkspaceManager) CreateWorkspace(ctx context.Context, req *CreateWorkspaceRequest) (*Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check limits
	if len(m.workspaces) >= m.config.MaxWorkspaces {
		return nil, fmt.Errorf("maximum workspaces (%d) reached", m.config.MaxWorkspaces)
	}

	// Generate ID
	id := generateID()

	// Apply template if specified
	config := req.Config
	if req.TemplateID != "" {
		template, err := m.GetTemplate(req.TemplateID)
		if err != nil {
			return nil, fmt.Errorf("template not found: %w", err)
		}
		if config == nil {
			config = template.Config
		} else {
			// Merge template config with request
			config = mergeConfig(template.Config, config)
		}
	}

	// Apply defaults
	if config == nil {
		config = &WorkspaceConfig{}
	}

	// Set default resources
	resources := req.Resources
	if resources == nil {
		resources = &ResourceQuota{
			CPUCores:    m.config.DefaultCPU,
			MemoryBytes: m.config.DefaultMemory,
			StorageBytes: m.config.DefaultStorage,
		}
	}

	// Create workspace
	ws := &Workspace{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Project:     req.Project,
		Owner:       req.Owner,
		State:       StatePending,
		Template:    req.TemplateID,
		Config:      config,
		Resources:   resources,
		Usage:      &ResourceUsage{},
		Labels:     req.Labels,
		Tags:        req.Tags,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		AccessedAt:  time.Now(),
		AutoDelete:  req.AutoDelete,
	}

	// Set expiration if TTL specified
	if req.TTL != 0 {
		ttl := req.TTL
		if ttl < 0 {
			ttl = 0 // Immediate expiration for testing
		}
		if ttl > m.config.MaxTTL {
			ttl = m.config.MaxTTL
		}
		expires := time.Now().Add(ttl)
		ws.ExpiresAt = &expires
	}

	// Generate namespace and network
	ws.Namespace = fmt.Sprintf("nexus-ws-%s", id)
	ws.Network = fmt.Sprintf("nexus-net-%s", id)

	// Run create hook
	if m.CreateHook != nil {
		if err := m.CreateHook(ctx, ws); err != nil {
			ws.State = StateFailed
			ws.StateReason = err.Error()
			m.addEvent(ws, "failed", "create_failed", err.Error())
			return ws, err
		}
	}

	m.workspaces[id] = ws
	m.addEvent(ws, "created", "created", "Workspace created")

	return ws, nil
}

// GetWorkspace retrieves a workspace by ID.
func (m *WorkspaceManager) GetWorkspace(id string) (*Workspace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	return ws, nil
}

// ListWorkspaces returns all workspaces.
func (m *WorkspaceManager) ListWorkspaces() []*Workspace {
	m.mu.RLock()
	defer m.mu.RUnlock()

	workspaces := make([]*Workspace, 0, len(m.workspaces))
	for _, ws := range m.workspaces {
		workspaces = append(workspaces, ws)
	}
	return workspaces
}

// ListWorkspacesByProject returns workspaces for a project.
func (m *WorkspaceManager) ListWorkspacesByProject(project string) []*Workspace {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var workspaces []*Workspace
	for _, ws := range m.workspaces {
		if ws.Project == project {
			workspaces = append(workspaces, ws)
		}
	}
	return workspaces
}

// ListWorkspacesByOwner returns workspaces owned by a user.
func (m *WorkspaceManager) ListWorkspacesByOwner(owner string) []*Workspace {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var workspaces []*Workspace
	for _, ws := range m.workspaces {
		if ws.Owner == owner {
			workspaces = append(workspaces, ws)
		}
	}
	return workspaces
}

// UpdateWorkspace updates a workspace's configuration.
func (m *WorkspaceManager) UpdateWorkspace(id string, updates map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}

	// Apply updates
	if name, ok := updates["name"].(string); ok {
		ws.Name = name
	}
	if desc, ok := updates["description"].(string); ok {
		ws.Description = desc
	}
	if labels, ok := updates["labels"].(map[string]string); ok {
		ws.Labels = labels
	}
	if tags, ok := updates["tags"].([]string); ok {
		ws.Tags = tags
	}
	if resources, ok := updates["resources"].(*ResourceQuota); ok {
		ws.Resources = resources
	}

	ws.UpdatedAt = time.Now()
	m.addEvent(ws, "updated", "configuration_changed", "Workspace updated")

	return nil
}

// StartWorkspace starts a workspace.
func (m *WorkspaceManager) StartWorkspace(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}

	if ws.State == StateRunning {
		return fmt.Errorf("workspace already running")
	}

	ws.State = StateCreating
	ws.UpdatedAt = time.Now()

	// Run start hook
	if m.StartHook != nil {
		if err := m.StartHook(ctx, ws); err != nil {
			ws.State = StateFailed
			ws.StateReason = err.Error()
			m.addEvent(ws, "failed", "start_failed", err.Error())
			return err
		}
	}

	ws.State = StateRunning
	ws.AccessedAt = time.Now()
	m.addEvent(ws, "started", "started", "Workspace started")

	return nil
}

// StopWorkspace stops a workspace.
func (m *WorkspaceManager) StopWorkspace(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}

	if ws.State == StateStopped {
		return fmt.Errorf("workspace already stopped")
	}

	ws.State = StateStopped
	ws.UpdatedAt = time.Now()

	// Run stop hook
	if m.StopHook != nil {
		m.StopHook(ctx, ws)
	}

	m.addEvent(ws, "stopped", "stopped", "Workspace stopped")
	return nil
}

// DeleteWorkspace deletes a workspace.
func (m *WorkspaceManager) DeleteWorkspace(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}

	// Run delete hook
	if m.DeleteHook != nil {
		m.DeleteHook(ctx, ws)
	}

	delete(m.workspaces, id)
	m.addEvent(ws, "deleted", "deleted", "Workspace deleted")

	return nil
}

// ArchiveWorkspace archives a workspace.
func (m *WorkspaceManager) ArchiveWorkspace(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}

	ws.State = StateArchived
	ws.UpdatedAt = time.Now()
	m.addEvent(ws, "archived", "archived", "Workspace archived")

	return nil
}

// GetWorkspaceEvents returns the event history for a workspace.
func (m *WorkspaceManager) GetWorkspaceEvents(id string) []WorkspaceEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.events[id]
}

// UpdateUsage updates resource usage for a workspace.
func (m *WorkspaceManager) UpdateUsage(id string, usage *ResourceUsage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ws, ok := m.workspaces[id]
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}

	ws.Usage = usage
	ws.Usage.UpdatedAt = time.Now()
	return nil
}

// RegisterTemplate registers a workspace template.
func (m *WorkspaceManager) RegisterTemplate(template *WorkspaceTemplate) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.templates) >= m.config.MaxTemplates {
		return fmt.Errorf("maximum templates (%d) reached", m.config.MaxTemplates)
	}

	if template.ID == "" {
		template.ID = generateID()
	}
	template.CreatedAt = time.Now()
	template.UpdatedAt = template.CreatedAt

	m.templates[template.ID] = template
	return nil
}

// GetTemplate retrieves a template by ID.
func (m *WorkspaceManager) GetTemplate(id string) (*WorkspaceTemplate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	template, ok := m.templates[id]
	if !ok {
		return nil, fmt.Errorf("template not found: %s", id)
	}
	return template, nil
}

// ListTemplates returns all templates.
func (m *WorkspaceManager) ListTemplates() []*WorkspaceTemplate {
	m.mu.RLock()
	defer m.mu.RUnlock()

	templates := make([]*WorkspaceTemplate, 0, len(m.templates))
	for _, t := range m.templates {
		templates = append(templates, t)
	}
	return templates
}

// DeleteTemplate removes a template.
func (m *WorkspaceManager) DeleteTemplate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.templates[id]; !ok {
		return fmt.Errorf("template not found: %s", id)
	}
	delete(m.templates, id)
	return nil
}

// CleanupExpired removes expired workspaces.
func (m *WorkspaceManager) CleanupExpired(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, ws := range m.workspaces {
		if ws.ExpiresAt != nil && now.After(*ws.ExpiresAt) {
			delete(m.workspaces, id)
			m.addEvent(ws, "deleted", "expired", "Workspace expired and deleted")
		}
	}
	return nil
}

// GetStats returns workspace statistics.
func (m *WorkspaceManager) GetStats() WorkspaceStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := WorkspaceStats{
		Total: len(m.workspaces),
	}

	for _, ws := range m.workspaces {
		switch ws.State {
		case StateRunning:
			stats.Running++
		case StateStopped:
			stats.Stopped++
		case StateArchived:
			stats.Archived++
		case StatePending, StateCreating, StateFailed:
			stats.Other++
		}
	}

	return stats
}

// Helper functions

func (m *WorkspaceManager) addEvent(ws *Workspace, eventType, reason, message string) {
	m.eventID++
	event := WorkspaceEvent{
		ID:          fmt.Sprintf("%s-%d", ws.ID, m.eventID),
		WorkspaceID: ws.ID,
		Type:        eventType,
		Reason:      reason,
		Message:     message,
		Timestamp:   time.Now(),
	}
	m.events[ws.ID] = append(m.events[ws.ID], event)
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func mergeConfig(template, override *WorkspaceConfig) *WorkspaceConfig {
	if override == nil {
		return template
	}

	merged := *template

	// Merge environment
	if override.Environment != nil {
		if merged.Environment == nil {
			merged.Environment = make(map[string]string)
		}
		for k, v := range override.Environment {
			merged.Environment[k] = v
		}
	}

	// Merge volumes (append)
	if len(override.Volumes) > 0 {
		merged.Volumes = append(merged.Volumes, override.Volumes...)
	}

	// Merge ports (append)
	if len(override.Ports) > 0 {
		merged.Ports = append(merged.Ports, override.Ports...)
	}

	// Override commands
	if len(override.InitCommand) > 0 {
		merged.InitCommand = override.InitCommand
	}
	if override.StartCommand != "" {
		merged.StartCommand = override.StartCommand
	}

	// Override health check
	if override.HealthCheck != nil {
		merged.HealthCheck = override.HealthCheck
	}

	// Override flags
	merged.Privileged = override.Privileged || merged.Privileged
	merged.ReadOnlyRoot = override.ReadOnlyRoot || merged.ReadOnlyRoot

	// Merge extensions
	if len(override.Extensions) > 0 {
		merged.Extensions = append(merged.Extensions, override.Extensions...)
	}

	return &merged
}

// CreateWorkspaceRequest is used to create a new workspace.
type CreateWorkspaceRequest struct {
	Name        string
	Description string
	Project     string
	Owner       string
	TemplateID  string
	Config      *WorkspaceConfig
	Resources   *ResourceQuota
	Labels      map[string]string
	Tags        []string
	TTL         time.Duration
	AutoDelete  bool
}

// WorkspaceStats holds statistics about workspaces.
type WorkspaceStats struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Stopped  int `json:"stopped"`
	Archived int `json:"archived"`
	Other    int `json:"other"`
}

// ResourceComparator compares resource usage against quotas.
type ResourceComparator struct{}

// IsOverQuota checks if usage exceeds quota.
func (c *ResourceComparator) IsOverQuota(usage *ResourceUsage, quota *ResourceQuota) []ResourceType {
	var exceeded []ResourceType

	if usage.CPUUsage > quota.CPUCores {
		exceeded = append(exceeded, ResourceCPU)
	}
	if usage.MemoryBytes > quota.MemoryBytes {
		exceeded = append(exceeded, ResourceMemory)
	}
	if usage.StorageBytes > quota.StorageBytes {
		exceeded = append(exceeded, ResourceStorage)
	}
	if usage.BandwidthBytes > quota.BandwidthBytes {
		exceeded = append(exceeded, ResourceNetwork)
	}

	return exceeded
}

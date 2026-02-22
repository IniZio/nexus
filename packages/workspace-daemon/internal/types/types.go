package types

import (
	"time"
)

type WorkspaceStatus int

const (
	StatusCreating WorkspaceStatus = iota
	StatusRunning
	StatusSleeping
	StatusStopped
	StatusError
)

func (s WorkspaceStatus) String() string {
	switch s {
	case StatusCreating:
		return "creating"
	case StatusRunning:
		return "running"
	case StatusSleeping:
		return "sleeping"
	case StatusStopped:
		return "stopped"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

type BackendType int

const (
	BackendDocker BackendType = iota
	BackendSprite
	BackendKubernetes
)

func (b BackendType) String() string {
	switch b {
	case BackendDocker:
		return "docker"
	case BackendSprite:
		return "sprite"
	case BackendKubernetes:
		return "kubernetes"
	default:
		return "unknown"
	}
}

func BackendTypeFromString(s string) BackendType {
	switch s {
	case "docker":
		return BackendDocker
	case "sprite":
		return BackendSprite
	case "kubernetes":
		return BackendKubernetes
	default:
		return BackendDocker
	}
}

func WorkspaceStatusFromString(s string) WorkspaceStatus {
	switch s {
	case "creating":
		return StatusCreating
	case "running":
		return StatusRunning
	case "sleeping":
		return StatusSleeping
	case "stopped":
		return StatusStopped
	case "error":
		return StatusError
	default:
		return StatusStopped
	}
}

type Workspace struct {
	ID           string
	Name         string
	DisplayName  string
	Status       WorkspaceStatus
	Backend      BackendType
	Repository   *Repository
	Branch       string
	Resources    *ResourceAllocation
	Ports        []PortMapping
	Config       *WorkspaceConfig
	Labels       map[string]string
	Annotations  map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
}

type Repository struct {
	URL            string
	Provider       string
	LocalPath      string
	DefaultBranch  string
	CurrentCommit  string
}

type ResourceAllocation struct {
	CPUCores     float64
	CPULimit     float64
	MemoryBytes int64
	MemoryLimit int64
	StorageBytes int64
}

type PortMapping struct {
	Name          string
	Protocol      string
	ContainerPort int32
	HostPort      int32
	Visibility    string
	URL           string
}

type WorkspaceConfig struct {
	Image            string
	DevcontainerPath string
	Env              map[string]string
	EnvFiles         []string
	Volumes          []VolumeConfig
	Services         []ServiceConfig
	Hooks            *WorkspaceHooks
	IdleTimeout      int32
	ShutdownBehavior string
}

type VolumeConfig struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

type ServiceConfig struct {
	Name      string
	Image     string
	Ports     []PortMapping
	Env       map[string]string
	Volumes   []VolumeConfig
	DependsOn []string
}

type WorkspaceHooks struct {
	PreCreate  []string
	PostCreate []string
	PreStart   []string
	PostStart  []string
	PreStop    []string
	PostStop   []string
}

type Operation struct {
	ID           string
	Status       string
	ErrorMessage string
	CreatedAt    time.Time
	CompletedAt  time.Time
}

type ResourceStats struct {
	WorkspaceID      string
	CPUUsagePercent   float64
	MemoryUsedBytes   int64
	MemoryLimitBytes  int64
	DiskUsedBytes     int64
	NetworkRxBytes    int64
	NetworkTxBytes    int64
	Timestamp         time.Time
}

type WorkspaceEvent struct {
	ID          string
	WorkspaceID string
	EventType   string
	Data        string
	ActorType   string
	ActorID     string
	OccurredAt  time.Time
}

type Snapshot struct {
	ID          string
	WorkspaceID string
	Name        string
	Description string
	SizeBytes   int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

type CreateWorkspaceRequest struct {
	Name           string
	DisplayName    string
	Backend        BackendType
	RepositoryURL  string
	Branch         string
	ResourceClass  string
	Config         *WorkspaceConfig
	Labels         map[string]string
	ForwardSSH     bool
	ID             string
	WorktreePath   string
}

type GetWorkspaceRequest struct {
	ID   string
	Name string
}

type ListWorkspacesRequest struct {
	StatusFilter  string
	BackendFilter string
	LabelSelector string
	PageSize      int32
	PageToken     string
}

type ListWorkspacesResponse struct {
	Workspaces     []*Workspace
	NextPageToken  string
	TotalCount     int32
}

type UpdateWorkspaceRequest struct {
	ID     string
	Config *WorkspaceConfig
	Labels map[string]string
}

type DeleteWorkspaceRequest struct {
	ID    string
	Force bool
}

type DeleteWorkspaceResponse struct {
	Success bool
}

type StartWorkspaceRequest struct {
	ID string
}

type StopWorkspaceRequest struct {
	ID             string
	TimeoutSeconds int32
}

type SwitchWorkspaceRequest struct {
	FromID string
	ToID   string
}

type SwitchWorkspaceResponse struct {
	Success          bool
	SwitchDurationMS int64
	ActiveWorkspace  *Workspace
}

package types

type WorkspaceStatusProto int32

const (
	WorkspaceStatusProto_WORKSPACE_STATUS_UNSPECIFIED WorkspaceStatusProto = 0
	WorkspaceStatusProto_WORKSPACE_STATUS_PENDING    WorkspaceStatusProto = 1
	WorkspaceStatusProto_WORKSPACE_STATUS_STOPPED    WorkspaceStatusProto = 2
	WorkspaceStatusProto_WORKSPACE_STATUS_RUNNING     WorkspaceStatusProto = 3
	WorkspaceStatusProto_WORKSPACE_STATUS_PAUSED      WorkspaceStatusProto = 4
	WorkspaceStatusProto_WORKSPACE_STATUS_ERROR       WorkspaceStatusProto = 5
	WorkspaceStatusProto_WORKSPACE_STATUS_DESTROYING  WorkspaceStatusProto = 6
	WorkspaceStatusProto_WORKSPACE_STATUS_DESTROYED   WorkspaceStatusProto = 7
)

type BackendTypeProto int32

const (
	BackendTypeProto_BACKEND_TYPE_UNSPECIFIED  BackendTypeProto = 0
	BackendTypeProto_BACKEND_TYPE_DOCKER       BackendTypeProto = 1
	BackendTypeProto_BACKEND_TYPE_SPRITE       BackendTypeProto = 2
	BackendTypeProto_BACKEND_TYPE_KUBERNETES    BackendTypeProto = 3
)

func (s WorkspaceStatus) ToProto() WorkspaceStatusProto {
	switch s {
	case StatusCreating:
		return WorkspaceStatusProto_WORKSPACE_STATUS_PENDING
	case StatusRunning:
		return WorkspaceStatusProto_WORKSPACE_STATUS_RUNNING
	case StatusSleeping:
		return WorkspaceStatusProto_WORKSPACE_STATUS_PAUSED
	case StatusStopped:
		return WorkspaceStatusProto_WORKSPACE_STATUS_STOPPED
	case StatusError:
		return WorkspaceStatusProto_WORKSPACE_STATUS_ERROR
	default:
		return WorkspaceStatusProto_WORKSPACE_STATUS_UNSPECIFIED
	}
}

func FromProtoStatus(s WorkspaceStatusProto) WorkspaceStatus {
	switch s {
	case WorkspaceStatusProto_WORKSPACE_STATUS_PENDING:
		return StatusCreating
	case WorkspaceStatusProto_WORKSPACE_STATUS_RUNNING:
		return StatusRunning
	case WorkspaceStatusProto_WORKSPACE_STATUS_PAUSED:
		return StatusSleeping
	case WorkspaceStatusProto_WORKSPACE_STATUS_STOPPED:
		return StatusStopped
	case WorkspaceStatusProto_WORKSPACE_STATUS_ERROR:
		return StatusError
	default:
		return StatusStopped
	}
}

func (b BackendType) ToProto() BackendTypeProto {
	switch b {
	case BackendDocker:
		return BackendTypeProto_BACKEND_TYPE_DOCKER
	case BackendSprite:
		return BackendTypeProto_BACKEND_TYPE_SPRITE
	case BackendKubernetes:
		return BackendTypeProto_BACKEND_TYPE_KUBERNETES
	default:
		return BackendTypeProto_BACKEND_TYPE_UNSPECIFIED
	}
}

func FromProtoBackend(b BackendTypeProto) BackendType {
	switch b {
	case BackendTypeProto_BACKEND_TYPE_DOCKER:
		return BackendDocker
	case BackendTypeProto_BACKEND_TYPE_SPRITE:
		return BackendSprite
	case BackendTypeProto_BACKEND_TYPE_KUBERNETES:
		return BackendKubernetes
	default:
		return BackendDocker
	}
}

type WorkspaceProto struct {
	Id           string
	Name         string
	DisplayName  string
	Status       WorkspaceStatusProto
	Backend      BackendTypeProto
	Repository   *RepositoryProto
	Branch       string
	Resources    *ResourceAllocationProto
	Ports        []*PortMappingProto
	Config       *WorkspaceConfigProto
	Labels       map[string]string
	Annotations  map[string]string
	CreatedAt    interface{}
	UpdatedAt    interface{}
	LastActiveAt interface{}
	ExpiresAt    interface{}
}

type RepositoryProto struct {
	Url           string
	Provider      string
	LocalPath     string
	DefaultBranch string
	CurrentCommit string
}

func (r *Repository) ToProto() *RepositoryProto {
	if r == nil {
		return nil
	}
	return &RepositoryProto{
		Url:           r.URL,
		Provider:      r.Provider,
		LocalPath:     r.LocalPath,
		DefaultBranch: r.DefaultBranch,
		CurrentCommit: r.CurrentCommit,
	}
}

func FromProtoRepository(p *RepositoryProto) *Repository {
	if p == nil {
		return nil
	}
	return &Repository{
		URL:            p.Url,
		Provider:       p.Provider,
		LocalPath:      p.LocalPath,
		DefaultBranch: p.DefaultBranch,
		CurrentCommit:  p.CurrentCommit,
	}
}

type ResourceAllocationProto struct {
	CpuCores     float64
	CpuLimit     float64
	MemoryBytes  int64
	MemoryLimit  int64
	StorageBytes int64
}

func (r *ResourceAllocation) ToProto() *ResourceAllocationProto {
	if r == nil {
		return nil
	}
	return &ResourceAllocationProto{
		CpuCores:     r.CPUCores,
		CpuLimit:     r.CPULimit,
		MemoryBytes:  r.MemoryBytes,
		MemoryLimit:  r.MemoryLimit,
		StorageBytes: r.StorageBytes,
	}
}

func FromProtoResources(p *ResourceAllocationProto) *ResourceAllocation {
	if p == nil {
		return nil
	}
	return &ResourceAllocation{
		CPUCores:     p.CpuCores,
		CPULimit:     p.CpuLimit,
		MemoryBytes:  p.MemoryBytes,
		MemoryLimit:  p.MemoryLimit,
		StorageBytes: p.StorageBytes,
	}
}

type PortMappingProto struct {
	Name          string
	Protocol      string
	ContainerPort int32
	HostPort      int32
	Visibility    string
	Url           string
}

func portsToProto(ports []PortMapping) []*PortMappingProto {
	result := make([]*PortMappingProto, len(ports))
	for i, p := range ports {
		result[i] = &PortMappingProto{
			Name:          p.Name,
			Protocol:      p.Protocol,
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
			Visibility:    p.Visibility,
			Url:           p.URL,
		}
	}
	return result
}

func portsFromProto(ports []*PortMappingProto) []PortMapping {
	result := make([]PortMapping, len(ports))
	for i, p := range ports {
		result[i] = PortMapping{
			Name:          p.Name,
			Protocol:      p.Protocol,
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
			Visibility:    p.Visibility,
			URL:           p.Url,
		}
	}
	return result
}

type WorkspaceConfigProto struct {
	Image            string
	DevcontainerPath string
	Env              map[string]string
	EnvFiles         []string
	Volumes          []*VolumeConfigProto
	Services         []*ServiceConfigProto
	Hooks            *WorkspaceHooksProto
	IdleTimeout      int32
	ShutdownBehavior string
}

func (c *WorkspaceConfig) ToProto() *WorkspaceConfigProto {
	if c == nil {
		return nil
	}
	return &WorkspaceConfigProto{
		Image:            c.Image,
		DevcontainerPath: c.DevcontainerPath,
		Env:              c.Env,
		EnvFiles:         c.EnvFiles,
		Volumes:          volumesToProto(c.Volumes),
		Services:         servicesToProto(c.Services),
		Hooks:            c.Hooks.ToProto(),
		IdleTimeout:      c.IdleTimeout,
		ShutdownBehavior: c.ShutdownBehavior,
	}
}

func FromProtoConfig(p *WorkspaceConfigProto) *WorkspaceConfig {
	if p == nil {
		return nil
	}
	return &WorkspaceConfig{
		Image:            p.Image,
		DevcontainerPath: p.DevcontainerPath,
		Env:              p.Env,
		EnvFiles:         p.EnvFiles,
		Volumes:          volumesFromProto(p.Volumes),
		Services:         servicesFromProto(p.Services),
		Hooks:            fromProtoHooks(p.Hooks),
		IdleTimeout:      p.IdleTimeout,
		ShutdownBehavior: p.ShutdownBehavior,
	}
}

type VolumeConfigProto struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

func volumesToProto(vols []VolumeConfig) []*VolumeConfigProto {
	result := make([]*VolumeConfigProto, len(vols))
	for i, v := range vols {
		result[i] = &VolumeConfigProto{
			Type:     v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}
	}
	return result
}

func volumesFromProto(vols []*VolumeConfigProto) []VolumeConfig {
	result := make([]VolumeConfig, len(vols))
	for i, v := range vols {
		result[i] = VolumeConfig{
			Type:     v.Type,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		}
	}
	return result
}

type ServiceConfigProto struct {
	Name      string
	Image     string
	Ports     []*PortMappingProto
	Env       map[string]string
	Volumes   []*VolumeConfigProto
	DependsOn []string
}

func servicesToProto(svcs []ServiceConfig) []*ServiceConfigProto {
	result := make([]*ServiceConfigProto, len(svcs))
	for i, s := range svcs {
		result[i] = &ServiceConfigProto{
			Name:      s.Name,
			Image:     s.Image,
			Ports:     portsToProto(s.Ports),
			Env:       s.Env,
			Volumes:   volumesToProto(s.Volumes),
			DependsOn: s.DependsOn,
		}
	}
	return result
}

func servicesFromProto(svcs []*ServiceConfigProto) []ServiceConfig {
	result := make([]ServiceConfig, len(svcs))
	for i, s := range svcs {
		result[i] = ServiceConfig{
			Name:      s.Name,
			Image:     s.Image,
			Ports:     portsFromProto(s.Ports),
			Env:       s.Env,
			Volumes:   volumesFromProto(s.Volumes),
			DependsOn: s.DependsOn,
		}
	}
	return result
}

type WorkspaceHooksProto struct {
	PreCreate  []string
	PostCreate []string
	PreStart   []string
	PostStart  []string
	PreStop    []string
	PostStop   []string
}

func (h *WorkspaceHooks) ToProto() *WorkspaceHooksProto {
	if h == nil {
		return nil
	}
	return &WorkspaceHooksProto{
		PreCreate:  h.PreCreate,
		PostCreate: h.PostCreate,
		PreStart:   h.PreStart,
		PostStart:  h.PostStart,
		PreStop:    h.PreStop,
		PostStop:   h.PostStop,
	}
}

func fromProtoHooks(p *WorkspaceHooksProto) *WorkspaceHooks {
	if p == nil {
		return nil
	}
	return &WorkspaceHooks{
		PreCreate:  p.PreCreate,
		PostCreate: p.PostCreate,
		PreStart:   p.PreStart,
		PostStart:  p.PostStart,
		PreStop:    p.PreStop,
		PostStop:   p.PostStop,
	}
}

func (w *Workspace) ToProto() *WorkspaceProto {
	if w == nil {
		return nil
	}
	return &WorkspaceProto{
		Id:           w.ID,
		Name:         w.Name,
		DisplayName:  w.DisplayName,
		Status:       w.Status.ToProto(),
		Backend:      w.Backend.ToProto(),
		Repository:   w.Repository.ToProto(),
		Branch:       w.Branch,
		Resources:    w.Resources.ToProto(),
		Ports:        portsToProto(w.Ports),
		Config:        w.Config.ToProto(),
		Labels:       w.Labels,
		Annotations:  w.Annotations,
	}
}

func FromProtoWorkspace(p *WorkspaceProto) *Workspace {
	if p == nil {
		return nil
	}
	return &Workspace{
		ID:           p.Id,
		Name:         p.Name,
		DisplayName:  p.DisplayName,
		Status:       FromProtoStatus(p.Status),
		Backend:      FromProtoBackend(p.Backend),
		Repository:   FromProtoRepository(p.Repository),
		Branch:       p.Branch,
		Resources:    FromProtoResources(p.Resources),
		Ports:        portsFromProto(p.Ports),
		Config:       FromProtoConfig(p.Config),
		Labels:       p.Labels,
		Annotations:  p.Annotations,
	}
}

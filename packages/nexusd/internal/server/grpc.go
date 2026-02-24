package server

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log"
)

type GRPCServer struct {
	address  string
	listener net.Listener
	server   *grpc.Server
	opts     []grpc.ServerOption
}

func NewGRPCServer(address string, opts ...grpc.ServerOption) *GRPCServer {
	return &GRPCServer{
		address: address,
		opts:    opts,
	}
}

func (s *GRPCServer) WithCredentials(creds credentials.TransportCredentials) *GRPCServer {
	s.opts = append(s.opts, grpc.Creds(creds))
	return s
}

func (s *GRPCServer) Start() error {
	lis, err := net.Listen("tcp", s.address)
	if err != nil {
		return err
	}

	s.listener = lis
	s.server = grpc.NewServer(s.opts...)

	go func() {
		if err := s.server.Serve(lis); err != nil {
			log.Printf("gRPC server stopped: %v", err)
		}
	}()

	return nil
}

func (s *GRPCServer) Stop() {
	if s.server != nil {
		s.server.GracefulStop()
	}
}

func (s *GRPCServer) Server() *grpc.Server {
	return s.server
}

func (s *GRPCServer) RegisterService(desc *grpc.ServiceDesc, impl interface{}) {
	s.server.RegisterService(desc, impl)
}

type WorkspaceServiceServer interface {
	CreateWorkspace(context.Context, *CreateWorkspaceRequest) (*Workspace, error)
	GetWorkspace(context.Context, *GetWorkspaceRequest) (*Workspace, error)
	ListWorkspaces(context.Context, *ListWorkspacesRequest) (*ListWorkspacesResponse, error)
	UpdateWorkspace(context.Context, *UpdateWorkspaceRequest) (*Workspace, error)
	DeleteWorkspace(context.Context, *DeleteWorkspaceRequest) (*DeleteWorkspaceResponse, error)
	StartWorkspace(context.Context, *StartWorkspaceRequest) (*Operation, error)
	StopWorkspace(context.Context, *StopWorkspaceRequest) (*Operation, error)
	GetResourceStats(context.Context, *GetStatsRequest) (*ResourceStats, error)
}

type CreateWorkspaceRequest struct {
	Name          string
	DisplayName   string
	Backend       int32
	RepositoryURL string
	Branch        string
	ResourceClass string
	Config        *WorkspaceConfig
	Labels        map[string]string
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
	Workspaces    []*Workspace
	NextPageToken string
	TotalCount    int32
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

type Operation struct {
	ID           string
	Status       string
	ErrorMessage string
	CreatedAt    int64
	CompletedAt  int64
}

type WorkspaceConfig struct {
	Image            string
	DevcontainerPath string
	Env              map[string]string
	EnvFiles         []string
}

type GetStatsRequest struct {
	WorkspaceID string
}

type ResourceStats struct {
	WorkspaceID      string
	CPUUsagePercent  float64
	MemoryUsedBytes  int64
	MemoryLimitBytes int64
	DiskUsedBytes    int64
	NetworkRxBytes   int64
	NetworkTxBytes   int64
	Timestamp        int64
}

type Workspace struct {
	ID          string
	Name        string
	DisplayName string
	Status      int32
	Backend     int32
}

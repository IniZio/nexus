package driver

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus/internal/openshell"
	pb "github.com/IniZio/nexus/internal/openshell/computedriverpb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Lifecycle is the narrow seam between the gRPC handler and the CH backend.
// S4 replaces noopLifecycle with a real implementation backed by openshell.Backend
// and openshell.ImageBuilder. The contract: Provision allocates and boots; Start/Stop
// resume/pause; Delete tears down and removes all state.
type Lifecycle interface {
	Provision(ctx context.Context, spec openshell.SandboxSpec) error
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
}

type noopLifecycle struct{}

func (noopLifecycle) Provision(_ context.Context, _ openshell.SandboxSpec) error { return nil }
func (noopLifecycle) Start(_ context.Context, _ string) error                    { return nil }
func (noopLifecycle) Stop(_ context.Context, _ string) error                     { return nil }
func (noopLifecycle) Delete(_ context.Context, _ string) error                   { return nil }

type Server struct {
	pb.UnimplementedComputeDriverServer
	store            Store
	lc               Lifecycle
	gatewayAddr      string
	rootfsStagingDir string
	rootfsMaxBytes   uint64
	mu               sync.Mutex
	watchers         []chan *pb.WatchSandboxesEvent
}

func New(store Store, lc Lifecycle, gatewayAddr, rootfsStagingDir string, rootfsMaxBytes uint64) *Server {
	if lc == nil {
		lc = noopLifecycle{}
	}
	return &Server{
		store:            store,
		lc:               lc,
		gatewayAddr:      gatewayAddr,
		rootfsStagingDir: rootfsStagingDir,
		rootfsMaxBytes:   rootfsMaxBytes,
	}
}

func (s *Server) GetCapabilities(_ context.Context, _ *pb.GetCapabilitiesRequest) (*pb.GetCapabilitiesResponse, error) {
	return &pb.GetCapabilitiesResponse{
		DriverName:                    "nexus-ch",
		DriverVersion:                 "0.1.0-dev",
		DefaultImage:                  "ghcr.io/nvidia/openshell-community/sandboxes/base:latest",
		GatewayManagesLifecycle:       true,
		SupportsSandboxAuthentication: false,
		RootfsTarStagingDir:           s.rootfsStagingDir,
		RootfsTarMaxBytes:             s.rootfsMaxBytes,
	}, nil
}

func (s *Server) AuthenticateSandbox(_ context.Context, _ *pb.AuthenticateSandboxRequest) (*pb.AuthenticateSandboxResponse, error) {
	return nil, status.Error(codes.Unimplemented, "AuthenticateSandbox not implemented")
}

func (s *Server) ValidateSandboxCreate(_ context.Context, _ *pb.ValidateSandboxCreateRequest) (*pb.ValidateSandboxCreateResponse, error) {
	return &pb.ValidateSandboxCreateResponse{}, nil
}

func (s *Server) GetSandbox(_ context.Context, req *pb.GetSandboxRequest) (*pb.GetSandboxResponse, error) {
	r, ok := s.store.Get(req.GetSandboxId())
	if !ok {
		r, ok = s.store.GetByName(req.GetName())
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %q not found", req.GetSandboxId())
	}
	return &pb.GetSandboxResponse{Sandbox: recordToProto(r)}, nil
}

func (s *Server) ListSandboxes(_ context.Context, _ *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	recs := s.store.List()
	out := make([]*pb.DriverSandbox, 0, len(recs))
	for _, r := range recs {
		out = append(out, recordToProto(r))
	}
	return &pb.ListSandboxesResponse{Sandboxes: out}, nil
}

func (s *Server) CreateSandbox(ctx context.Context, req *pb.CreateSandboxRequest) (*pb.CreateSandboxResponse, error) {
	proto := req.GetSandbox()
	spec := proto.GetSpec()
	tmpl := spec.GetTemplate()

	image := tmpl.GetImage()
	if image == "" {
		image = "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
	}

	env := make(map[string]string)
	for k, v := range tmpl.GetEnvironment() {
		env[k] = v
	}
	for k, v := range spec.GetEnvironment() {
		env[k] = v
	}

	var wi WorkloadIdentityRecord
	if w := spec.GetWorkloadIdentity(); w != nil {
		wi = WorkloadIdentityRecord{User: w.GetUser(), Group: w.GetGroup()}
	}

	r := SandboxRecord{
		ID:                   proto.GetId(),
		Name:                 proto.GetName(),
		Workspace:            proto.GetWorkspace(),
		Phase:                PhaseProvisioning,
		CreatedAt:            time.Now(),
		LaunchAuthentication: spec.GetLaunchAuthentication(),
		WorkloadIdentity:     wi,
		Spec: SandboxSpec{
			Image:    image,
			Command:  spec.GetCommand(),
			TTY:      spec.GetTty(),
			Env:      env,
			Mounts:   parseMounts(tmpl.GetDriverConfig()),
			Token:    spec.GetSandboxToken(),
			LogLevel: spec.GetLogLevel(),
		},
	}

	if err := s.store.Put(r); err != nil {
		return nil, status.Errorf(codes.Internal, "store: %v", err)
	}
	s.broadcast(sandboxEvent(r))

	osSpec := toOSSpec(r, proto.GetId(), proto.GetName(), 0, s.gatewayAddr, spec)
	go s.asyncProvision(r, osSpec)
	return &pb.CreateSandboxResponse{}, nil
}

func (s *Server) asyncProvision(r SandboxRecord, spec openshell.SandboxSpec) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.lc.Provision(ctx, spec); err != nil {
		slog.Error("provision failed", "id", r.ID, "err", err)
		r.Phase = PhaseFailed
	} else {
		r.Phase = PhaseReady
	}
	_ = s.store.Put(r)
	s.broadcast(sandboxEvent(r))
}

func (s *Server) StartSandbox(ctx context.Context, req *pb.StartSandboxRequest) (*pb.StartSandboxResponse, error) {
	r, ok := s.resolve(req.GetSandboxId(), req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %q not found", req.GetSandboxId())
	}
	r.Phase = PhaseProvisioning
	_ = s.store.Put(r)
	s.broadcast(sandboxEvent(r))

	go func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.lc.Start(ctx2, r.ID); err != nil {
			slog.Error("start failed", "id", r.ID, "err", err)
			r.Phase = PhaseFailed
		} else {
			r.Phase = PhaseReady
		}
		_ = s.store.Put(r)
		s.broadcast(sandboxEvent(r))
	}()
	return &pb.StartSandboxResponse{}, nil
}

func (s *Server) StopSandbox(ctx context.Context, req *pb.StopSandboxRequest) (*pb.StopSandboxResponse, error) {
	r, ok := s.resolve(req.GetSandboxId(), req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %q not found", req.GetSandboxId())
	}
	if err := s.lc.Stop(ctx, r.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "stop: %v", err)
	}
	r.Phase = PhaseStopped
	_ = s.store.Put(r)
	s.broadcast(sandboxEvent(r))
	return &pb.StopSandboxResponse{}, nil
}

func (s *Server) DeleteSandbox(ctx context.Context, req *pb.DeleteSandboxRequest) (*pb.DeleteSandboxResponse, error) {
	r, ok := s.resolve(req.GetSandboxId(), req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %q not found", req.GetSandboxId())
	}
	s.broadcast(deletingEvent(r))
	if err := s.lc.Delete(ctx, r.ID); err != nil {
		slog.Warn("delete lifecycle failed (continuing)", "id", r.ID, "err", err)
	}
	s.store.Delete(r.ID)
	s.broadcast(deletedEvent(r.ID))
	return &pb.DeleteSandboxResponse{Deleted: true}, nil
}

func (s *Server) WatchSandboxes(_ *pb.WatchSandboxesRequest, stream pb.ComputeDriver_WatchSandboxesServer) error {
	ch := make(chan *pb.WatchSandboxesEvent, 64)
	s.mu.Lock()
	s.watchers = append(s.watchers, ch)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		for i, w := range s.watchers {
			if w == ch {
				s.watchers = append(s.watchers[:i], s.watchers[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()

	for _, r := range s.store.List() {
		if err := stream.Send(sandboxEvent(r)); err != nil {
			return err
		}
	}

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

func (s *Server) EnsureWorkspace(_ context.Context, _ *pb.EnsureWorkspaceRequest) (*pb.EnsureWorkspaceResponse, error) {
	return &pb.EnsureWorkspaceResponse{}, nil
}

func (s *Server) DeleteWorkspace(_ context.Context, _ *pb.DeleteWorkspaceRequest) (*pb.DeleteWorkspaceResponse, error) {
	return &pb.DeleteWorkspaceResponse{}, nil
}

func (s *Server) broadcast(ev *pb.WatchSandboxesEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.watchers {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) resolve(id, name string) (SandboxRecord, bool) {
	if r, ok := s.store.Get(id); ok {
		return r, true
	}
	return s.store.GetByName(name)
}

func recordToProto(r SandboxRecord) *pb.DriverSandbox {
	var cond *pb.DriverCondition
	var deleting bool
	switch r.Phase {
	case PhaseReady:
		cond = &pb.DriverCondition{Type: "Ready", Status: "True", Reason: "BackendReady", Message: "nexus-ch sandbox running"}
	case PhaseStopped:
		cond = &pb.DriverCondition{Type: "Ready", Status: "False", Reason: "Stopped", Message: "nexus-ch sandbox stopped"}
	case PhaseDeleting:
		cond = &pb.DriverCondition{Type: "Ready", Status: "False", Reason: "Deleting", Message: "nexus-ch sandbox being removed"}
		deleting = true
	case PhaseFailed:
		cond = &pb.DriverCondition{Type: "Ready", Status: "False", Reason: "ProvisionFailed", Message: "nexus-ch sandbox provisioning failed"}
	default:
		cond = &pb.DriverCondition{Type: "Ready", Status: "False", Reason: "Starting", Message: "nexus-ch sandbox provisioning"}
	}
	return &pb.DriverSandbox{
		Id:        r.ID,
		Name:      r.Name,
		Workspace: r.Workspace,
		Status: &pb.DriverSandboxStatus{
			Name:       r.Name,
			Conditions: []*pb.DriverCondition{cond},
			Deleting:   deleting,
		},
	}
}

func sandboxEvent(r SandboxRecord) *pb.WatchSandboxesEvent {
	return &pb.WatchSandboxesEvent{
		Payload: &pb.WatchSandboxesEvent_Sandbox{
			Sandbox: &pb.WatchSandboxesSandboxEvent{Sandbox: recordToProto(r)},
		},
	}
}

func deletingEvent(r SandboxRecord) *pb.WatchSandboxesEvent {
	r.Phase = PhaseDeleting
	return sandboxEvent(r)
}

func deletedEvent(id string) *pb.WatchSandboxesEvent {
	return &pb.WatchSandboxesEvent{
		Payload: &pb.WatchSandboxesEvent_Deleted{
			Deleted: &pb.WatchSandboxesDeletedEvent{SandboxId: id},
		},
	}
}

func toOSSpec(r SandboxRecord, id, name string, generation uint64, gatewayAddr string, spec *pb.DriverSandboxSpec) openshell.SandboxSpec {
	var mounts []openshell.Mount
	for _, m := range r.Spec.Mounts {
		mounts = append(mounts, openshell.Mount{HostPath: m.HostPath, GuestPath: m.GuestPath, ReadOnly: m.ReadOnly})
	}
	var res openshell.Resources
	if rr := spec.GetTemplate().GetResources(); rr != nil {
		res.BootMemBytes = parseMemBytes(rr.GetMemoryRequest())
		res.CeilingMemBytes = parseMemBytes(rr.GetMemoryLimit())
		res.VCPUs = parseCPU(rr.GetCpuLimit())
	}
	return openshell.SandboxSpec{
		ID:          id,
		Name:        name,
		ImageRef:    r.Spec.Image,
		Command:     r.Spec.Command,
		Env:         r.Spec.Env,
		Mounts:      mounts,
		Resources:   res,
		GatewayAddr: gatewayAddr,
		Generation:  generation,
	}
}

func parseMounts(cfg *structpb.Struct) []MountRecord {
	if cfg == nil {
		return nil
	}
	raw, err := cfg.MarshalJSON()
	if err != nil {
		return nil
	}
	var obj struct {
		Mounts []struct {
			Source   string `json:"source"`
			Target   string `json:"target"`
			ReadOnly bool   `json:"read_only"`
		} `json:"mounts"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	out := make([]MountRecord, 0, len(obj.Mounts))
	for _, m := range obj.Mounts {
		out = append(out, MountRecord{HostPath: m.Source, GuestPath: m.Target, ReadOnly: m.ReadOnly})
	}
	return out
}

func parseMemBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	multipliers := map[string]int64{
		"Ki": 1024, "Mi": 1024 * 1024, "Gi": 1024 * 1024 * 1024,
		"K": 1000, "M": 1000 * 1000, "G": 1000 * 1000 * 1000,
	}
	for suffix, mult := range multipliers {
		if strings.HasSuffix(s, suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, suffix), 10, 64)
			if err == nil {
				return n * mult
			}
		}
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseCPU(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 32)
		if err == nil {
			return int32((n + 999) / 1000)
		}
	}
	n, _ := strconv.ParseInt(s, 10, 32)
	return int32(n)
}

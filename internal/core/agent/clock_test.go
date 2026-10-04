package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type clockServer struct {
	agentpb.UnimplementedAgentServiceServer
	prev int64
	got  int64
	err  error
}

func (s *clockServer) SetClock(_ context.Context, r *agentpb.SetClockRequest) (*agentpb.SetClockResponse, error) {
	s.got = r.UnixNanos
	return &agentpb.SetClockResponse{PreviousUnixNanos: s.prev}, s.err
}

func TestSetGuestClock_ComputesSkew(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	td := newTestDialer()
	srv := &clockServer{prev: now.Add(-90 * time.Second).UnixNano()}
	startGRPCServer(t, td.controlLis, srv)
	skew, err := agent.SetGuestClock(context.Background(), agent.NewClient(td, domain.NewSandboxID()), now)
	if err != nil {
		t.Fatal(err)
	}
	if skew != -90_000 {
		t.Errorf("skew = %d, want -90000", skew)
	}
	if srv.got != now.UnixNano() {
		t.Errorf("sent %d, want %d", srv.got, now.UnixNano())
	}
}

func TestSetGuestClock_Error(t *testing.T) {
	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &clockServer{err: status.Error(codes.PermissionDenied, "no")})
	if _, err := agent.SetGuestClock(context.Background(), agent.NewClient(td, domain.NewSandboxID()), time.Now()); err == nil {
		t.Fatal("want error")
	}
}

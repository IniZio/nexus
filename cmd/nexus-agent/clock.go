package main

import (
	"context"
	"time"

	"github.com/mdlayher/vsock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/IniZio/nexus/internal/core/agent/agentpb"
)

// vsockCIDHost is VMADDR_CID_HOST: the only peer allowed to set the clock.
const vsockCIDHost = 2

// setClockFn and nowFn are the injectable seams for SetClock.
var (
	setClockFn = setRealtimeClock
	nowFn      = time.Now
)

// requireHostPeer rejects callers that are not the host vsock CID, so a guest
// process dialing the agent over vsock loopback cannot move the clock.
func requireHostPeer(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "no peer")
	}
	a, ok := p.Addr.(*vsock.Addr)
	if !ok || a.ContextID != vsockCIDHost {
		return status.Error(codes.PermissionDenied, "set clock: host only")
	}
	return nil
}

// SetClock sets CLOCK_REALTIME to the host-provided time.
func (cs *controlServer) SetClock(ctx context.Context, req *agentpb.SetClockRequest) (*agentpb.SetClockResponse, error) {
	if err := requireHostPeer(ctx); err != nil {
		return nil, err
	}
	if req.GetUnixNanos() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "unix_nanos required")
	}
	prev := nowFn().UnixNano()
	if err := setClockFn(time.Unix(0, req.GetUnixNanos())); err != nil {
		return nil, status.Errorf(codes.Internal, "set clock: %v", err)
	}
	return &agentpb.SetClockResponse{PreviousUnixNanos: prev}, nil
}

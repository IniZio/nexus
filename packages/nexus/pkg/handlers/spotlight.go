package handlers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
)

var discoverPublishedPorts = compose.DiscoverPublishedPorts

type SpotlightExposeParams struct {
	Spec spotlight.ExposeSpec `json:"spec"`
}

type SpotlightListParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type SpotlightCloseParams struct {
	ID string `json:"id"`
}

type SpotlightExposeResult struct {
	Forward *spotlight.Forward `json:"forward"`
}

type SpotlightListResult struct {
	Forwards []*spotlight.Forward `json:"forwards"`
}

type SpotlightCloseResult struct {
	Closed bool `json:"closed"`
}

type SpotlightApplyDefaultsParams struct {
	WorkspaceID string `json:"workspaceId"`
}

type SpotlightApplyDefaultsResult struct {
	Forwards []*spotlight.Forward `json:"forwards"`
}

type SpotlightApplyComposePortsParams struct {
	WorkspaceID string `json:"workspaceId"`
}

type SpotlightApplyComposePortsError struct {
	Service    string `json:"service"`
	HostPort   int    `json:"hostPort"`
	TargetPort int    `json:"targetPort"`
	Message    string `json:"message"`
}

type SpotlightApplyComposePortsResult struct {
	Forwards []*spotlight.Forward              `json:"forwards"`
	Errors   []SpotlightApplyComposePortsError `json:"errors"`
}

func HandleSpotlightExpose(ctx context.Context, params json.RawMessage, mgr *spotlight.Manager) (*SpotlightExposeResult, *rpckit.RPCError) {
	var p SpotlightExposeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	fwd, err := mgr.Expose(ctx, p.Spec)
	if err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	return &SpotlightExposeResult{Forward: fwd}, nil
}

func HandleSpotlightList(_ context.Context, params json.RawMessage, mgr *spotlight.Manager) (*SpotlightListResult, *rpckit.RPCError) {
	var p SpotlightListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, rpckit.ErrInvalidParams
		}
	}

	all := mgr.List(p.WorkspaceID)
	return &SpotlightListResult{Forwards: all}, nil
}

func HandleSpotlightClose(_ context.Context, params json.RawMessage, mgr *spotlight.Manager) (*SpotlightCloseResult, *rpckit.RPCError) {
	var p SpotlightCloseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	closed := mgr.Close(p.ID)
	if !closed {
		return nil, rpckit.ErrInvalidParams
	}

	return &SpotlightCloseResult{Closed: true}, nil
}

func HandleSpotlightApplyDefaults(ctx context.Context, params json.RawMessage, rootPath string, mgr *spotlight.Manager) (*SpotlightApplyDefaultsResult, *rpckit.RPCError) {
	_ = ctx
	_ = mgr
	var p SpotlightApplyDefaultsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	if p.WorkspaceID == "" || rootPath == "" {
		return nil, rpckit.ErrInvalidParams
	}
	return &SpotlightApplyDefaultsResult{Forwards: []*spotlight.Forward{}}, nil
}

func HandleSpotlightApplyComposePorts(ctx context.Context, params json.RawMessage, rootPath string, mgr *spotlight.Manager) (*SpotlightApplyComposePortsResult, *rpckit.RPCError) {
	var p SpotlightApplyComposePortsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}
	if p.WorkspaceID == "" || rootPath == "" {
		return nil, rpckit.ErrInvalidParams
	}

	published, err := discoverPublishedPorts(ctx, rootPath)
	if err != nil {
		if errors.Is(err, compose.ErrComposeFileNotFound) {
			return &SpotlightApplyComposePortsResult{
				Forwards: []*spotlight.Forward{},
				Errors:   []SpotlightApplyComposePortsError{},
			}, nil
		}
		return nil, rpckit.ErrInvalidParams
	}

	result := &SpotlightApplyComposePortsResult{
		Forwards: make([]*spotlight.Forward, 0, len(published)),
		Errors:   make([]SpotlightApplyComposePortsError, 0),
	}

	for _, entry := range published {
		host := entry.HostIP
		if host == "" {
			host = "127.0.0.1"
		}

		fwd, exposeErr := mgr.Expose(ctx, spotlight.ExposeSpec{
			WorkspaceID: p.WorkspaceID,
			Service:     entry.Service,
			RemotePort:  entry.TargetPort,
			LocalPort:   entry.HostPort,
			Host:        host,
		})
		if exposeErr != nil {
			result.Errors = append(result.Errors, SpotlightApplyComposePortsError{
				Service:    entry.Service,
				HostPort:   entry.HostPort,
				TargetPort: entry.TargetPort,
				Message:    exposeErr.Error(),
			})
			continue
		}
		result.Forwards = append(result.Forwards, fwd)
	}

	return result, nil
}

package rpc

import (
	"context"
	"encoding/json"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
)

type Handler func(ctx context.Context, msgID string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError)

type Registry struct {
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

func (r *Registry) Register(method string, h Handler) {
	r.handlers[method] = h
}

func (r *Registry) Dispatch(ctx context.Context, method, msgID string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError) {
	h, ok := r.handlers[method]
	if !ok {
		return nil, rpckit.ErrMethodNotFound
	}
	return h(ctx, msgID, params, conn)
}

func TypedRegister[Req any, Res any](r *Registry, method string, h func(ctx context.Context, req Req) (Res, *rpckit.RPCError)) {
	r.Register(method, func(ctx context.Context, _ string, raw json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		var req Req
		norm := raw
		if len(norm) == 0 || string(norm) == "null" {
			norm = []byte("{}")
		}
		if err := json.Unmarshal(norm, &req); err != nil {
			return nil, rpckit.ErrInvalidParams
		}
		return h(ctx, req)
	})
}

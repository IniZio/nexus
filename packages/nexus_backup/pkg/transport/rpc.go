package transport

import (
	"context"
	"encoding/json"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
)

// TypedRegister registers a strongly-typed handler with a Registry.
// It unmarshals params into TParams, calls fn, and returns the result.
func TypedRegister[TParams, TResult any](r Registry, method string, fn func(ctx context.Context, req TParams) (TResult, *rpckit.RPCError)) {
	r.Register(method, func(ctx context.Context, _ string, params json.RawMessage, _ any) (interface{}, *rpckit.RPCError) {
		var p TParams
		norm := params
		if len(norm) == 0 || string(norm) == "null" {
			norm = []byte("{}")
		}
		if err := json.Unmarshal(norm, &p); err != nil {
			return nil, &rpckit.RPCError{Code: rpckit.ErrInvalidParams.Code, Message: err.Error()}
		}
		return fn(ctx, p)
	})
}

package registry

import (
	"context"
	"encoding/json"
	"fmt"
)

type HandlerFunc func(ctx context.Context, params json.RawMessage) (any, error)

type Registry interface {
	Register(method string, handler HandlerFunc)
}

type MapRegistry struct {
	handlers map[string]HandlerFunc
}

func NewMapRegistry() *MapRegistry {
	return &MapRegistry{handlers: make(map[string]HandlerFunc)}
}

func (r *MapRegistry) Register(method string, handler HandlerFunc) {
	r.handlers[method] = handler
}

func (r *MapRegistry) Dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	h, ok := r.handlers[method]
	if !ok {
		return nil, fmt.Errorf("method not found: %s", method)
	}
	return h(ctx, params)
}

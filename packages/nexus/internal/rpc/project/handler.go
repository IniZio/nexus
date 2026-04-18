// Package project provides JSON-RPC handlers for project management.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/domain/project"
	rpce "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// Handler exposes project.* RPC methods.
type Handler struct {
	repo project.Repository
}

// New creates a Handler backed by the given repository.
func New(repo project.Repository) *Handler {
	return &Handler{repo: repo}
}

// Register wires all project.* methods into reg.
func (h *Handler) Register(reg registry.Registry) {
	reg.Register("project.list", h.list)
	reg.Register("project.create", h.create)
	reg.Register("project.get", h.get)
	reg.Register("project.remove", h.remove)
}

// ── DTOs ─────────────────────────────────────────────────────────────────────

type listReq struct{}
type listRes struct {
	Projects []*project.Project `json:"projects"`
}

type createReq struct {
	Name    string `json:"name"`
	RepoURL string `json:"repoUrl"`
}
type createRes struct {
	Project *project.Project `json:"project"`
}

type getReq struct {
	ID string `json:"id"`
}
type getRes struct {
	Project *project.Project `json:"project"`
}

type removeReq struct {
	ID string `json:"id"`
}
type removeRes struct {
	Removed bool `json:"removed"`
}

// ── helpers ──────────────────────────────────────────────────────────────────

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	norm := raw
	if len(norm) == 0 || string(norm) == "null" {
		norm = []byte("{}")
	}
	if err := json.Unmarshal(norm, &v); err != nil {
		return v, rpce.InvalidParams("invalid params: " + err.Error())
	}
	return v, nil
}

func mapErr(err error) error {
	if errors.Is(err, project.ErrNotFound) {
		return rpce.NotFound("project not found")
	}
	return rpce.Internal(fmt.Sprintf("internal error: %v", err))
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (h *Handler) list(ctx context.Context, raw json.RawMessage) (any, error) {
	all, err := h.repo.List(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	if all == nil {
		all = []*project.Project{}
	}
	return &listRes{Projects: all}, nil
}

func (h *Handler) create(ctx context.Context, raw json.RawMessage) (any, error) {
	req, err := decode[createReq](raw)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, rpce.InvalidParams("name is required")
	}
	now := time.Now().UTC()
	p := &project.Project{
		ID:        deriveID(req.RepoURL, name),
		Name:      name,
		RepoURL:   req.RepoURL,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.repo.Create(ctx, p); err != nil {
		return nil, mapErr(err)
	}
	return &createRes{Project: p}, nil
}

func (h *Handler) get(ctx context.Context, raw json.RawMessage) (any, error) {
	req, err := decode[getReq](raw)
	if err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, rpce.InvalidParams("id is required")
	}
	p, err := h.repo.Get(ctx, req.ID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &getRes{Project: p}, nil
}

func (h *Handler) remove(ctx context.Context, raw json.RawMessage) (any, error) {
	req, err := decode[removeReq](raw)
	if err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, rpce.InvalidParams("id is required")
	}
	if err := h.repo.Delete(ctx, req.ID); err != nil {
		return nil, mapErr(err)
	}
	return &removeRes{Removed: true}, nil
}

func deriveID(repoURL, name string) string {
	s := repoURL + ":" + name
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("proj-%08x", h)
}

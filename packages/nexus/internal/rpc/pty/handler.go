// Package pty provides JSON-RPC handlers for PTY session management.
package pty

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	appty "github.com/inizio/nexus/packages/nexus/internal/app/pty"
	rpcerrors "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// Handler provides JSON-RPC dispatch for PTY operations.
type Handler struct {
	reg *appty.Registry
}

// New creates a new Handler backed by the given Registry.
func New(reg *appty.Registry) *Handler {
	return &Handler{reg: reg}
}

// Register wires all pty.* methods into the given RPC registry.
func (h *Handler) Register(r registry.Registry) {
	r.Register("pty.create", h.create)
	r.Register("pty.list", h.list)
	r.Register("pty.resize", h.resize)
	r.Register("pty.rename", h.rename)
	r.Register("pty.close", h.close)
}

// ── DTOs ─────────────────────────────────────────────────────────────────────

type createParams struct {
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Shell       string   `json:"shell"`
	Args        []string `json:"args"`
	WorkDir     string   `json:"workDir"`
	Cols        int      `json:"cols"`
	Rows        int      `json:"rows"`
}

type listParams struct {
	WorkspaceID string `json:"workspaceId"`
}

type resizeParams struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type renameParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

type closeParams struct {
	SessionID string `json:"sessionId"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (h *Handler) create(ctx context.Context, raw json.RawMessage) (any, error) {
	var p createParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, rpcerrors.InvalidParams(err.Error())
	}
	if p.WorkspaceID == "" {
		return nil, rpcerrors.InvalidParams("workspaceId is required")
	}

	shell := p.Shell
	if shell == "" {
		shell = defaultShell()
	}
	cols, rows := p.Cols, p.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	args := p.Args
	if len(args) == 0 {
		args = []string{"-l"}
	}

	cmd := exec.CommandContext(ctx, shell, args...) //nolint:gosec
	if p.WorkDir != "" {
		cmd.Dir = p.WorkDir
	}

	s := &appty.Session{
		ID:          fmt.Sprintf("pty-%d", time.Now().UnixNano()),
		WorkspaceID: p.WorkspaceID,
		Name:        p.Name,
		Shell:       shell,
		WorkDir:     p.WorkDir,
		Cols:        cols,
		Rows:        rows,
		Cmd:         cmd,
		Done:        make(chan struct{}),
		CreatedAt:   time.Now(),
	}

	h.reg.Register(s)
	return s.Info(), nil
}

func (h *Handler) list(_ context.Context, raw json.RawMessage) (any, error) {
	var p listParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, rpcerrors.InvalidParams(err.Error())
	}
	if p.WorkspaceID == "" {
		return nil, rpcerrors.InvalidParams("workspaceId is required")
	}
	return h.reg.ListByWorkspace(p.WorkspaceID), nil
}

func (h *Handler) resize(_ context.Context, raw json.RawMessage) (any, error) {
	var p resizeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, rpcerrors.InvalidParams(err.Error())
	}
	s := h.reg.Get(p.SessionID)
	if s == nil {
		return nil, rpcerrors.NotFound("session not found")
	}
	s.Mu.Lock()
	s.Cols = p.Cols
	s.Rows = p.Rows
	s.Mu.Unlock()
	return map[string]bool{"ok": true}, nil
}

func (h *Handler) rename(_ context.Context, raw json.RawMessage) (any, error) {
	var p renameParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, rpcerrors.InvalidParams(err.Error())
	}
	if !h.reg.Rename(p.SessionID, p.Name) {
		return nil, rpcerrors.NotFound("session not found")
	}
	return map[string]bool{"ok": true}, nil
}

func (h *Handler) close(_ context.Context, raw json.RawMessage) (any, error) {
	var p closeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, rpcerrors.InvalidParams(err.Error())
	}
	s := h.reg.Get(p.SessionID)
	if s == nil {
		return nil, rpcerrors.NotFound("session not found")
	}
	if s.Cmd != nil && s.Cmd.Process != nil {
		_ = s.Cmd.Process.Kill()
	}
	h.reg.Unregister(p.SessionID)
	return map[string]bool{"ok": true}, nil
}

func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

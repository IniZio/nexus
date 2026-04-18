// Package fs provides JSON-RPC handlers for file-system operations on workspace paths.
package fs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	rpce "github.com/inizio/nexus/packages/nexus/internal/rpc/errors"
	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// Handler exposes fs.* RPC methods operating within a workspace root directory.
type Handler struct {
	root string
}

// New creates a Handler that sandboxes all operations under root.
func New(root string) *Handler {
	return &Handler{root: root}
}

// Register wires all fs.* methods into reg.
func (h *Handler) Register(reg registry.Registry) {
	reg.Register("fs.readFile", h.readFile)
	reg.Register("fs.writeFile", h.writeFile)
	reg.Register("fs.exists", h.exists)
	reg.Register("fs.readdir", h.readdir)
	reg.Register("fs.mkdir", h.mkdir)
	reg.Register("fs.rm", h.rm)
	reg.Register("fs.stat", h.stat)
}

// ── DTOs ─────────────────────────────────────────────────────────────────────

type readFileParams struct {
	Path     string `json:"path"`
	Encoding string `json:"encoding"`
}

type readFileResult struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Size     int64  `json:"size"`
}

type writeFileParams struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type writeFileResult struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type existsParams struct {
	Path string `json:"path"`
}

type existsResult struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path"`
}

type readdirParams struct {
	Path string `json:"path"`
}

type dirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}

type readdirResult struct {
	Entries []dirEntry `json:"entries"`
	Path    string     `json:"path"`
}

type mkdirParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

type rmParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

type statParams struct {
	Path string `json:"path"`
}

type statResult struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
	Stats   struct {
		IsFile      bool   `json:"isFile"`
		IsDirectory bool   `json:"isDirectory"`
		Size        int64  `json:"size"`
		Mtime       string `json:"mtime"`
		Ctime       string `json:"ctime"`
		Mode        int    `json:"mode"`
	} `json:"stats"`
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (h *Handler) securePath(userPath string) (string, error) {
	if userPath == "" || userPath == "." {
		return h.root, nil
	}
	if filepath.IsAbs(userPath) {
		return "", rpce.InvalidParams("absolute paths not allowed")
	}
	clean := filepath.Clean(userPath)
	full := filepath.Join(h.root, clean)
	if !strings.HasPrefix(full, h.root) {
		return "", rpce.InvalidParams("path traversal not allowed")
	}
	return full, nil
}

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

// ── handlers ─────────────────────────────────────────────────────────────────

func (h *Handler) readFile(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[readFileParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, rpce.InvalidParams("path is required")
	}
	safe, err := h.securePath(p.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(safe)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpce.NotFound("file not found: " + p.Path)
		}
		return nil, rpce.Internal(err.Error())
	}
	enc := "utf8"
	if p.Encoding != "" {
		enc = p.Encoding
	}
	content := string(data)
	if enc == "base64" {
		content = base64.StdEncoding.EncodeToString(data)
	}
	return &readFileResult{Content: content, Encoding: enc, Size: int64(len(data))}, nil
}

func (h *Handler) writeFile(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[writeFileParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, rpce.InvalidParams("path is required")
	}
	safe, err := h.securePath(p.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(safe), 0755); err != nil {
		return nil, rpce.Internal(err.Error())
	}
	content := []byte(p.Content)
	if p.Encoding == "base64" {
		content, err = base64.StdEncoding.DecodeString(p.Content)
		if err != nil {
			return nil, rpce.InvalidParams("invalid base64 content")
		}
	}
	if err := os.WriteFile(safe, content, 0644); err != nil {
		return nil, rpce.Internal(err.Error())
	}
	info, err := os.Stat(safe)
	if err != nil {
		return nil, rpce.Internal(err.Error())
	}
	return &writeFileResult{OK: true, Path: p.Path, Size: info.Size()}, nil
}

func (h *Handler) exists(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[existsParams](raw)
	if err != nil {
		return nil, err
	}
	safe, err := h.securePath(p.Path)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(safe)
	return &existsResult{Exists: !errors.Is(statErr, os.ErrNotExist), Path: p.Path}, nil
}

func (h *Handler) readdir(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[readdirParams](raw)
	if err != nil {
		return nil, err
	}
	path := p.Path
	if path == "" {
		path = "."
	}
	safe, err := h.securePath(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(safe)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpce.NotFound("directory not found: " + path)
		}
		return nil, rpce.Internal(err.Error())
	}
	result := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, dirEntry{
			Name:  e.Name(),
			Path:  filepath.Join(path, e.Name()),
			IsDir: e.IsDir(),
			Size:  info.Size(),
			Mode:  info.Mode().String(),
		})
	}
	return &readdirResult{Entries: result, Path: path}, nil
}

func (h *Handler) mkdir(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[mkdirParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, rpce.InvalidParams("path is required")
	}
	safe, err := h.securePath(p.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(safe, 0755); err != nil {
		return nil, rpce.Internal(err.Error())
	}
	return &writeFileResult{OK: true, Path: p.Path}, nil
}

func (h *Handler) rm(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[rmParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, rpce.InvalidParams("path is required")
	}
	safe, err := h.securePath(p.Path)
	if err != nil {
		return nil, err
	}
	if p.Recursive {
		if err := os.RemoveAll(safe); err != nil {
			return nil, rpce.Internal(err.Error())
		}
	} else {
		if err := os.Remove(safe); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, rpce.NotFound("not found: " + p.Path)
			}
			return nil, rpce.Internal(err.Error())
		}
	}
	return &writeFileResult{OK: true, Path: p.Path}, nil
}

func (h *Handler) stat(_ context.Context, raw json.RawMessage) (any, error) {
	p, err := decode[statParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, rpce.InvalidParams("path is required")
	}
	safe, err := h.securePath(p.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(safe)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpce.NotFound("not found: " + p.Path)
		}
		return nil, rpce.Internal(err.Error())
	}
	mtime := info.ModTime().Format("2006-01-02T15:04:05Z07:00")
	res := &statResult{
		Name:    filepath.Base(safe),
		Path:    p.Path,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: mtime,
	}
	res.Stats.IsFile = !info.IsDir()
	res.Stats.IsDirectory = info.IsDir()
	res.Stats.Size = info.Size()
	res.Stats.Mtime = mtime
	res.Stats.Ctime = mtime
	res.Stats.Mode = int(info.Mode())
	return res, nil
}

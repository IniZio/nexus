package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	rpckit "github.com/nexus/nexus/packages/workspace-daemon/pkg/rpcerrors"
	"github.com/nexus/nexus/packages/workspace-daemon/pkg/workspace"
)

type ReadFileParams struct {
	Path     string `json:"path"`
	Encoding string `json:"encoding"`
}

type WriteFileParams struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type ExistsParams struct {
	Path string `json:"path"`
}

type ReaddirParams struct {
	Path string `json:"path"`
}

type MkdirParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

type RmParams struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

type StatParams struct {
	Path string `json:"path"`
}

type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}

type ReadFileResult struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Size     int64  `json:"size"`
}

type WriteFileResult struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type ExistsResult struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path"`
}

type ReaddirResult struct {
	Entries []DirEntry `json:"entries"`
	Path    string     `json:"path"`
}

type StatResult struct {
	Stats struct {
		IsFile      bool   `json:"isFile"`
		IsDirectory bool   `json:"isDirectory"`
		Size        int64  `json:"size"`
		Mtime       string `json:"mtime"`
		Ctime       string `json:"ctime"`
		Mode        int    `json:"mode"`
	} `json:"stats"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

func HandleReadFile(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*ReadFileResult, *rpckit.RPCError) {
	var p ReadFileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	safePath, err := ws.SecurePath(p.Path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	content, err := os.ReadFile(safePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpckit.ErrFileNotFound
		}
		return nil, rpckit.ErrInternalError
	}

	encoding := "utf8"
	if p.Encoding != "" {
		encoding = p.Encoding
	}

	return &ReadFileResult{
		Content:  string(content),
		Encoding: encoding,
		Size:     int64(len(content)),
	}, nil
}

func HandleWriteFile(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*WriteFileResult, *rpckit.RPCError) {
	var p WriteFileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if p.Path == "" {
		return nil, rpckit.ErrInvalidParams
	}

	safePath, err := ws.SecurePath(p.Path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, rpckit.ErrInternalError
	}

	content := p.Content
	if p.Encoding == "base64" {
		data, err := decodeBase64(p.Content)
		if err != nil {
			return nil, rpckit.ErrInvalidParams
		}
		content = string(data)
	}

	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return nil, rpckit.ErrInternalError
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return nil, rpckit.ErrInternalError
	}

	return &WriteFileResult{
		OK:   true,
		Path: p.Path,
		Size: info.Size(),
	}, nil
}

func HandleExists(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*ExistsResult, *rpckit.RPCError) {
	var p ExistsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	safePath, err := ws.SecurePath(p.Path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	_, err = os.Stat(safePath)
	exists := !errors.Is(err, os.ErrNotExist)

	return &ExistsResult{
		Exists: exists,
		Path:   p.Path,
	}, nil
}

func HandleReaddir(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*ReaddirResult, *rpckit.RPCError) {
	var p ReaddirParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	path := "."
	if p.Path != "" {
		path = p.Path
	}

	safePath, err := ws.SecurePath(path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpckit.ErrFileNotFound
		}
		return nil, rpckit.ErrInternalError
	}

	dirEntries := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		dirEntries = append(dirEntries, DirEntry{
			Name:  entry.Name(),
			Path:  entryPath,
			IsDir: entry.IsDir(),
			Size:  info.Size(),
			Mode:  info.Mode().String(),
		})
	}

	return &ReaddirResult{
		Entries: dirEntries,
		Path:    path,
	}, nil
}

func HandleMkdir(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*WriteFileResult, *rpckit.RPCError) {
	var p MkdirParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if p.Path == "" {
		return nil, rpckit.ErrInvalidParams
	}

	safePath, err := ws.SecurePath(p.Path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	opts := os.ModePerm
	if !p.Recursive {
		opts = 0755
	}

	if err := os.MkdirAll(safePath, opts); err != nil {
		return nil, rpckit.ErrInternalError
	}

	return &WriteFileResult{
		OK:   true,
		Path: p.Path,
	}, nil
}

func HandleRm(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*WriteFileResult, *rpckit.RPCError) {
	var p RmParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	if p.Path == "" {
		return nil, rpckit.ErrInvalidParams
	}

	safePath, err := ws.SecurePath(p.Path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	if p.Recursive {
		if err := os.RemoveAll(safePath); err != nil {
			return nil, rpckit.ErrInternalError
		}
	} else {
		if err := os.Remove(safePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, rpckit.ErrFileNotFound
			}
			return nil, rpckit.ErrInternalError
		}
	}

	return &WriteFileResult{
		OK:   true,
		Path: p.Path,
	}, nil
}

func HandleStat(ctx context.Context, params json.RawMessage, ws *workspace.Workspace) (*StatResult, *rpckit.RPCError) {
	var p StatParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, rpckit.ErrInvalidParams
	}

	safePath, err := ws.SecurePath(p.Path)
	if err != nil {
		return nil, rpckit.ErrInvalidPath
	}

	info, err := os.Stat(safePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rpckit.ErrFileNotFound
		}
		return nil, rpckit.ErrInternalError
	}

	return &StatResult{
		Name:    filepath.Base(safePath),
		Path:    p.Path,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Format("2006-01-02T15:04:05Z07:00"),
		Stats: struct {
			IsFile      bool   `json:"isFile"`
			IsDirectory bool   `json:"isDirectory"`
			Size        int64  `json:"size"`
			Mtime       string `json:"mtime"`
			Ctime       string `json:"ctime"`
			Mode        int    `json:"mode"`
		}{
			IsFile:      !info.IsDir(),
			IsDirectory: info.IsDir(),
			Size:        info.Size(),
			Mtime:       info.ModTime().Format("2006-01-02T15:04:05Z07:00"),
			Ctime:       info.ModTime().Format("2006-01-02T15:04:05Z07:00"),
			Mode:        int(info.Mode()),
		},
	}, nil
}

func decodeBase64(s string) ([]byte, error) {
	return []byte{}, nil
}

func getDirEntries(entries []fs.DirEntry) []DirEntry {
	result := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, _ := entry.Info()
		result = append(result, DirEntry{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
			Mode:  info.Mode().String(),
		})
	}
	return result
}

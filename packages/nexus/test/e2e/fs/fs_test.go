//go:build e2e

package fs_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/test/e2e/harness"
)

func TestFS(t *testing.T) {
	h := harness.New(t)

	// 1. List /tmp (relative to daemon root /, so path is "tmp")
	var readdirRes struct {
		Entries []struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			IsDir bool   `json:"is_dir"`
		} `json:"entries"`
		Path string `json:"path"`
	}
	h.MustCall("fs.readdir", map[string]any{"path": "tmp"}, &readdirRes)
	if readdirRes.Path == "" {
		t.Fatal("fs.readdir: expected non-empty path in result")
	}

	// 2. Write a file under tmp
	filename := fmt.Sprintf("tmp/nexus-e2e-fs-%d.txt", time.Now().UnixNano())
	content := "hello from nexus fs e2e test"
	var writeRes struct {
		OK   bool   `json:"ok"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	h.MustCall("fs.writeFile", map[string]any{
		"path":    filename,
		"content": content,
	}, &writeRes)
	if !writeRes.OK {
		t.Fatalf("fs.writeFile: expected ok=true, got false")
	}
	if writeRes.Size != int64(len(content)) {
		t.Fatalf("fs.writeFile: expected size %d, got %d", len(content), writeRes.Size)
	}

	// 3. Read it back
	var readRes struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
	}
	h.MustCall("fs.readFile", map[string]any{"path": filename}, &readRes)
	if readRes.Content != content {
		t.Fatalf("fs.readFile: expected %q, got %q", content, readRes.Content)
	}

	// 4. Stat the file
	var statRes struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	h.MustCall("fs.stat", map[string]any{"path": filename}, &statRes)
	if statRes.IsDir {
		t.Fatal("fs.stat: expected file, got directory")
	}
	if statRes.Size != int64(len(content)) {
		t.Fatalf("fs.stat: expected size %d, got %d", len(content), statRes.Size)
	}

	// 5. Clean up
	var rmRes struct {
		OK bool `json:"ok"`
	}
	h.MustCall("fs.rm", map[string]any{"path": filename}, &rmRes)
	if !rmRes.OK {
		t.Fatal("fs.rm: expected ok=true")
	}
}

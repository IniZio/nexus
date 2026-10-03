package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func hasStaleWarning(res *gosdk.CallToolResult) bool {
	if !res.IsError {
		return false
	}
	for _, c := range res.Content {
		if tc, ok := c.(*gosdk.TextContent); ok && strings.Contains(tc.Text, "binary was replaced since this MCP server started") {
			return true
		}
	}
	return false
}

func TestStaleWatcher_RefusesAfterBinaryReplaced(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "nexus")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	sw := newStaleWatcher(exe)

	ctx := context.Background()
	ct, st := gosdk.NewInMemoryTransports()
	ss, err := newServer(&stubService{}, sw).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := gosdk.NewClient(&gosdk.Implementation{Name: "c", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cs.Close(); ss.Wait() }()

	res := callTool(t, cs, "sandbox_list", nil)
	if hasStaleWarning(res) {
		t.Fatal("unexpected stale warning before replacement")
	}

	tmp := exe + ".new"
	if err := os.WriteFile(tmp, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		t.Fatal(err)
	}

	res = callTool(t, cs, "sandbox_list", nil)
	if !hasStaleWarning(res) {
		t.Fatalf("expected stale warning after rename-replace; content=%v", res.Content)
	}
}

func TestStaleWatcher_MissingPathNeverStale(t *testing.T) {
	if newStaleWatcher(filepath.Join(t.TempDir(), "nope")).stale() {
		t.Fatal("unknown binary must not report stale")
	}
}

func TestStaleWatcher_DeletedExeStale(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "nexus")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	sw := newStaleWatcher(exe)
	sw.exeLink = func() string { return exe }
	if sw.stale() {
		t.Fatal("fresh binary must not be stale")
	}
	sw.exeLink = func() string { return exe + " (deleted)" }
	if !sw.stale() {
		t.Fatal("deleted exe must be stale")
	}
}

func TestStaleWatcher_FreshBinaryExecutes(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "nexus")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	sw := newStaleWatcher(exe)
	sw.exeLink = func() string { return exe }
	ctx := context.Background()
	ct, st := gosdk.NewInMemoryTransports()
	ss, err := newServer(&stubService{}, sw).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := gosdk.NewClient(&gosdk.Implementation{Name: "c", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cs.Close(); ss.Wait() }()
	if res := callTool(t, cs, "sandbox_list", nil); res.IsError {
		t.Fatalf("fresh binary must execute; content=%v", res.Content)
	}
	sw.exeLink = func() string { return exe + " (deleted)" }
	if res := callTool(t, cs, "sandbox_list", nil); !hasStaleWarning(res) {
		t.Fatalf("deleted exe must refuse; content=%v", res.Content)
	}
}

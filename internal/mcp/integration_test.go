package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/IniZio/nexus3/internal/mcp"
)

// TestMCPIntegration_stdio builds the nexus3 binary and runs it as an MCP
// subprocess over stdio. It performs:
//   - MCP initialize handshake (implicit in client.Connect)
//   - tools/list — asserts the sandbox lifecycle tools are present
//   - tools/call sandbox_list — asserts the response is a valid JSON array
//
// The test is hermetic: XDG_STATE_HOME is set to a temp dir so the store
// uses an empty directory and no real sandboxes exist. No VM or hypervisor
// is involved.
func TestMCPIntegration_stdio(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in -short mode")
	}

	// ── Build the binary ──────────────────────────────────────────────────────
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "nexus3")
	build := exec.Command("go", "build", "-o", binPath, "github.com/IniZio/nexus3/cmd/nexus3")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// ── Launch the MCP subprocess ─────────────────────────────────────────────
	// XDG_STATE_HOME is set to a temp dir so the file store starts empty and
	// the test needs no network, VMs, or any real infrastructure.
	stateDir := t.TempDir()

	cmd := exec.Command(binPath, "mcp")
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+stateDir)
	transport := &gosdk.CommandTransport{Command: cmd}

	client := gosdk.NewClient(&gosdk.Implementation{Name: "nexus3-test", Version: "v0"}, nil)
	ctx := context.Background()
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer cs.Close()

	// ── Verify initialization ─────────────────────────────────────────────────
	init := cs.InitializeResult()
	if init.ServerInfo.Name != "nexus3" {
		t.Errorf("server name: want %q, got %q", "nexus3", init.ServerInfo.Name)
	}

	// ── tools/list ────────────────────────────────────────────────────────────
	wantTools := map[string]bool{
		"sandbox_create": false,
		"sandbox_list":   false,
		"sandbox_start":  false,
		"sandbox_stop":   false,
		"sandbox_pause":  false,
		"sandbox_resume": false,
		"sandbox_remove": false,
	}
	if init.Capabilities.Tools == nil {
		t.Fatal("server advertises no tools capability")
	}
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools(): %v", err)
		}
		if _, ok := wantTools[tool.Name]; ok {
			wantTools[tool.Name] = true
		}
	}
	for name, seen := range wantTools {
		if !seen {
			t.Errorf("expected tool %q not listed by server", name)
		}
	}

	// ── tools/call sandbox_list ───────────────────────────────────────────────
	res, err := cs.CallTool(ctx, &gosdk.CallToolParams{Name: "sandbox_list"})
	if err != nil {
		t.Fatalf("CallTool(sandbox_list): %v", err)
	}
	if res.IsError {
		t.Fatalf("sandbox_list returned tool error: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("sandbox_list: no content in result")
	}
	tc, ok := res.Content[0].(*gosdk.TextContent)
	if !ok {
		t.Fatalf("sandbox_list: expected *TextContent, got %T", res.Content[0])
	}

	// The result is a Response envelope; data must be an empty JSON array.
	var env mcp.Response
	if err := json.Unmarshal([]byte(tc.Text), &env); err != nil {
		t.Fatalf("sandbox_list envelope unmarshal: %v (text=%q)", err, tc.Text)
	}
	if !env.OK {
		t.Fatalf("sandbox_list envelope not ok: %+v", env.Error)
	}
	dataBytes, _ := json.Marshal(env.Data)
	var list []json.RawMessage
	if err := json.Unmarshal(dataBytes, &list); err != nil {
		t.Fatalf("sandbox_list data is not valid JSON array: %v (data=%q)", err, dataBytes)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list from blank store, got %d items", len(list))
	}
}

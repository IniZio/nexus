package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newTestHerdrService builds an in-memory service for herdr plugin tests.
func newTestHerdrService(t *testing.T) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return service.New(st, fake.New(), lifecycle.New())
}

func TestHerdrPluginABI(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	err := runHerdrPlugin(context.Background(), []string{"abi"}, out)
	if err != nil {
		t.Fatalf("abi: unexpected error: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != "1" {
		t.Errorf("abi output: want %q, got %q", "1", got)
	}
}

func TestHerdrPluginContextCwd(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace_cwd":"/tmp/test"}`)

	var stdout bytes.Buffer
	err := herdrPluginContextCwd(&stdout)
	if err != nil {
		t.Fatalf("context-cwd: unexpected error: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != "/tmp/test" {
		t.Errorf("context-cwd output: want %q, got %q", "/tmp/test", got)
	}
}

func TestHerdrPluginContextCwd_missing(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")

	var stdout bytes.Buffer
	err := herdrPluginContextCwd(&stdout)
	if err == nil {
		t.Fatal("context-cwd: expected error when HERDR_PLUGIN_CONTEXT_JSON is unset, got nil")
	}
}

func TestHerdrPluginContextCwd_malformed(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `not-json`)

	var stdout bytes.Buffer
	err := herdrPluginContextCwd(&stdout)
	if err == nil {
		t.Fatal("context-cwd: expected error for malformed JSON, got nil")
	}
}

func TestSealEnv_stripsHerdr(t *testing.T) {
	env := []string{
		"HERDR_BIN_PATH=/usr/bin/herdr",
		"HERDR_PANE_ID=pane-123",
		"HERDR_WORKSPACE_ID=ws-456",
		"HERDR_ENV=1",
		"NEXUS3_WORKSPACE=proj/box",
		"HOME=/home/user",
		"PATH=/usr/bin:/bin",
	}
	sealed := sealEnv(env)

	for _, kv := range sealed {
		if strings.HasPrefix(kv, "HERDR_") {
			t.Errorf("sealEnv: HERDR_* entry leaked: %q", kv)
		}
	}

	wantPresent := []string{"NEXUS3_WORKSPACE=proj/box", "HOME=/home/user", "PATH=/usr/bin:/bin"}
	for _, want := range wantPresent {
		found := false
		for _, kv := range sealed {
			if kv == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("sealEnv: expected %q to be retained", want)
		}
	}
}

func TestHerdrPluginWorkspaces_empty(t *testing.T) {
	svc := newTestHerdrService(t)

	var stdout bytes.Buffer
	err := herdrPluginWorkspaces(context.Background(), &stdout, svc)
	if err != nil {
		t.Fatalf("workspaces: unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("workspaces: expected empty output, got %q", stdout.String())
	}
}

func TestHerdrPluginWorkspaces_nonEmpty(t *testing.T) {
	svc := newTestHerdrService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, "myproj", "mybox", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var stdout bytes.Buffer
	if err := herdrPluginWorkspaces(ctx, &stdout, svc); err != nil {
		t.Fatalf("workspaces: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "myproj/mybox") {
		t.Errorf("workspaces output: expected to contain %q, got %q", "myproj/mybox", out)
	}
}

func TestHerdrPluginDoctor(t *testing.T) {
	var stdout bytes.Buffer
	err := herdrPluginDoctor(&stdout)
	if err != nil {
		t.Fatalf("doctor: unexpected error: %v", err)
	}
	out := stdout.String()
	if len(out) == 0 {
		t.Error("doctor: expected non-empty output")
	}
	// Must report the ABI version.
	if !strings.Contains(out, "1") {
		t.Errorf("doctor output: expected ABI version %q, got %q", "1", out)
	}
}

func TestHerdrPluginLogs(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	err := runHerdrPlugin(context.Background(), []string{"logs"}, out)
	if err != nil {
		t.Fatalf("logs: unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "not yet implemented") {
		t.Errorf("logs: expected stub message, got %q", stdout.String())
	}
}

func TestHerdrPlugin_unknownSubcommand(t *testing.T) {
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"nonexistent"}, out)
	if err == nil {
		t.Fatal("expected error for unknown subcommand, got nil")
	}
}

func TestHerdrPlugin_noSubcommand(t *testing.T) {
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{}, out)
	if err == nil {
		t.Fatal("expected error for missing subcommand, got nil")
	}
}

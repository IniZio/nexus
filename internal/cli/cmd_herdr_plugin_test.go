package cli

import (
	"bytes"
	"context"
	"os"
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

func TestResolveDockerfilePath_standardContainerfile(t *testing.T) {
	dir := t.TempDir()
	nexusDir := dir + "/.nexus"
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cf := nexusDir + "/Containerfile"
	if err := os.WriteFile(cf, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, warn, err := resolveDockerfilePath(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != cf {
		t.Errorf("resolved: want %q, got %q", cf, resolved)
	}
	if warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}
}

func TestResolveDockerfilePath_dockerfileFallback(t *testing.T) {
	dir := t.TempDir()
	nexusDir := dir + "/.nexus"
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	df := nexusDir + "/Dockerfile"
	if err := os.WriteFile(df, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, warn, err := resolveDockerfilePath(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != df {
		t.Errorf("resolved: want %q, got %q", df, resolved)
	}
	if warn == "" {
		t.Error("expected a warning for Dockerfile fallback, got none")
	}
}

func TestResolveDockerfilePath_neitherExists(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveDockerfilePath(dir, "")
	if err == nil {
		t.Fatal("expected error when no Containerfile/Dockerfile found, got nil")
	}
	// Error message should name both tried paths.
	msg := err.Error()
	if !strings.Contains(msg, "Containerfile") {
		t.Errorf("error should mention Containerfile, got: %q", msg)
	}
	if !strings.Contains(msg, "Dockerfile") {
		t.Errorf("error should mention Dockerfile, got: %q", msg)
	}
}

func TestResolveDockerfilePath_overrideExists(t *testing.T) {
	dir := t.TempDir()
	override := dir + "/MyDockerfile"
	if err := os.WriteFile(override, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, warn, err := resolveDockerfilePath(dir, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != override {
		t.Errorf("resolved: want %q, got %q", override, resolved)
	}
	if warn == "" {
		t.Error("expected non-standard warning for override path")
	}
}

func TestResolveDockerfilePath_overrideMissing(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveDockerfilePath(dir, dir+"/nonexistent")
	if err == nil {
		t.Fatal("expected error for missing override path, got nil")
	}
}

func TestDeriveHandleFromContext(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/home/user/myproject", "local/myproject"},
		{"/tmp/foo-bar", "local/foo-bar"},
		{"/", "local/project"},
		{".", "local/project"},
	}
	for _, tc := range tests {
		got := deriveHandleFromContext(tc.in)
		if got != tc.want {
			t.Errorf("deriveHandleFromContext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHerdrPluginContextCwdValue_set(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace_cwd":"/workspace/proj"}`)
	got := herdrPluginContextCwdValue()
	if got != "/workspace/proj" {
		t.Errorf("got %q, want %q", got, "/workspace/proj")
	}
}

func TestHerdrPluginContextCwdValue_unset(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	got := herdrPluginContextCwdValue()
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

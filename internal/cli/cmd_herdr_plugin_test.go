package cli

import (
	"bytes"
	"context"
	"errors"
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

// TestBuildLaunchBootOpts_noEgress asserts the plain launch path carries no
// egress configuration at all: no allowlist, and — as on every launch path —
// no CLI-side broker.
func TestBuildLaunchBootOpts_noEgress(t *testing.T) {
	opts := buildLaunchBootOpts("myimage:latest", t.TempDir(), false)
	if len(opts.AllowedHosts) != 0 {
		t.Errorf("agentEgress=false: AllowedHosts must be empty, got %v", opts.AllowedHosts)
	}
	if opts.OpenEgress {
		t.Error("agentEgress=false: OpenEgress must stay false")
	}
}

// TestBuildLaunchBootOpts_agentEgress asserts the two things CreateAndBoot is
// actually responsible for on the egress path — freezing the allowlist onto the
// Envelope, and NOT wiring a credential broker.
//
// The absent broker is the load-bearing assertion. An earlier version of this
// path set Broker/Seeder/UseAgentSeed here and a test asserted they were set;
// that test passed for the entire time the feature was non-functional, because
// those options start no proxy. The perimeter is a process (the detached
// supervisor), and it mints its own placeholders after it re-boots the VM — a
// CLI-side broker can only mint a second, conflicting set.
func TestBuildLaunchBootOpts_agentEgress(t *testing.T) {
	opts := buildLaunchBootOpts("myimage:latest", t.TempDir(), true)
	if len(opts.AllowedHosts) == 0 {
		t.Error("agentEgress=true: AllowedHosts must be frozen onto the Envelope")
	}
	if opts.OpenEgress {
		t.Error("agentEgress=true: agent sandboxes must never get open egress (D-PD-33)")
	}
	if opts.Broker != nil {
		t.Error("agentEgress=true: no CLI-side broker — the supervisor owns credential minting")
	}
	if opts.Seeder != nil {
		t.Error("agentEgress=true: no CLI-side seeder — the supervisor seeds the guest after its reboot")
	}
	if opts.UseAgentSeed {
		t.Error("agentEgress=true: UseAgentSeed would seed placeholders the supervisor reboot discards")
	}
}

// TestLaunchCredSourcedArgv asserts the guest command sources the credential
// env file the supervisor seeded, and that the caller's argv survives intact as
// the shell's positional parameters.
func TestLaunchCredSourcedArgv(t *testing.T) {
	argv := []string{"/usr/local/bin/claude", "-p", "hello world", "--model", "m"}
	got := launchCredSourcedArgv(argv)

	if got[0] != "/bin/sh" || got[1] != "-c" {
		t.Fatalf("must exec through a shell, got %v", got[:2])
	}
	if !strings.Contains(got[2], service.GuestCredEnvPath) {
		t.Errorf("script must source %s, got:\n%s", service.GuestCredEnvPath, got[2])
	}
	if !strings.Contains(got[2], "set -a") {
		t.Error("script must export the sourced vars (set -a), or the agent never sees the placeholder")
	}
	if !strings.Contains(got[2], `exec "$@"`) {
		t.Error(`script must exec "$@" so the child's exit status is the launch exit status`)
	}
	// got[3] is $0; the caller's argv must follow verbatim from got[4].
	if diff := len(got) - 4; diff != len(argv) {
		t.Fatalf("expected %d positional args after $0, got %d: %v", len(argv), diff, got)
	}
	for i, want := range argv {
		if got[4+i] != want {
			t.Errorf("positional arg %d: got %q, want %q", i, got[4+i], want)
		}
	}
}

// TestHerdrPluginLaunch_flagParsing verifies that --agent-egress can appear
// before the image-ref and that the bare launch path (no flag) is unchanged.
// Neither case boots a VM; we only assert UsageError shapes.
func TestHerdrPluginLaunch_flagParsing(t *testing.T) {
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)

	// No image-ref after --agent-egress → UsageError (not a different error).
	err := runHerdrPlugin(context.Background(), []string{"launch", "--agent-egress"}, out)
	if err == nil {
		t.Fatal("launch --agent-egress (no image-ref): expected UsageError, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("launch --agent-egress (no image-ref): expected *UsageError, got %T: %v", err, err)
	}

	// No image-ref (plain path) → UsageError too.
	err2 := runHerdrPlugin(context.Background(), []string{"launch"}, out)
	if err2 == nil {
		t.Fatal("launch (no args): expected UsageError, got nil")
	}
}

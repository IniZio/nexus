package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"

	"github.com/IniZio/nexus3/internal/core/domain"
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

// TestHerdrPluginABI asserts the binary reports the SAME ABI the plugin
// declares in plugins/herdr/abi. The expected value is READ FROM THAT FILE,
// never restated here: build.sh compares those two, and a test that hardcodes
// its own third copy would go green while the pair it is meant to protect
// drifted apart. That is not hypothetical — the herdr command-group rename
// changed the plugin-facing CLI surface without bumping the ABI, so a binary
// that had lost the `herdr` verb still passed the probe, and every pane
// silently fell back to /root.
func TestHerdrPluginABI(t *testing.T) {
	declared, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "abi"))
	if err != nil {
		t.Fatalf("read plugins/herdr/abi: %v", err)
	}
	want := strings.TrimSpace(string(declared))

	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	if err := runHerdrPlugin(context.Background(), []string{"abi"}, out); err != nil {
		t.Fatalf("abi: unexpected error: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != want {
		t.Errorf("binary reports ABI %q but plugins/herdr/abi declares %q — "+
			"bump herdrPluginABIVersion and plugins/herdr/abi together, or "+
			"build.sh will refuse to install the plugin", got, want)
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
	// An overlay pane that renders NOTHING is indistinguishable from one that
	// crashed. With no sandboxes the pane must still show its header and say
	// what to do next, so "empty" reads as a state rather than a failure.
	out := stdout.String()
	if !strings.Contains(out, "WORKSPACE") {
		t.Errorf("workspaces: empty overlay is missing its header, got %q", out)
	}
	if !strings.Contains(out, "no sandboxes") {
		t.Errorf("workspaces: empty overlay does not tell the operator what to do, got %q", out)
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
	// Read the expected ABI from the canonical file so the assertion is
	// independent of herdrPluginABIVersion. If the constant is flipped
	// without updating the file (or vice versa), the test goes RED.
	abiDecl, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "abi"))
	if err != nil {
		t.Fatalf("read plugins/herdr/abi: %v", err)
	}
	wantABI := strings.TrimSpace(string(abiDecl))

	var stdout bytes.Buffer
	if err := herdrPluginDoctor(&stdout); err != nil {
		t.Fatalf("doctor: unexpected error: %v", err)
	}
	out := stdout.String()
	if len(out) == 0 {
		t.Error("doctor: expected non-empty output")
	}
	// Must report the ABI version on the exact line "plugin ABI:     <version>".
	wantLine := "plugin ABI:     " + wantABI
	if !strings.Contains(out, wantLine) {
		t.Errorf("doctor output: expected %q, got %q", wantLine, out)
	}
}

// TestHerdrPluginLogs verifies "__herdr-plugin logs" delegates to the real
// `nexus3 log` implementation (runLog) rather than the old stub message.
func TestHerdrPluginLogs(t *testing.T) {
	_, sb, stateDir := newLogTestSandbox(t)
	writeLog(t, stateDir, "hello\nworld\n")

	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	err := runHerdrPlugin(context.Background(), []string{"logs", sb.Handle()}, out)
	if err != nil {
		t.Fatalf("logs: unexpected error: %v", err)
	}
	if stdout.String() != "hello\nworld\n" {
		t.Errorf("logs: got %q, want %q", stdout.String(), "hello\nworld\n")
	}
}

// TestHerdrPluginLogs_MissingRef verifies the delegation still enforces
// runLog's own usage contract (a ref is required) instead of silently
// succeeding the way the old stub did.
func TestHerdrPluginLogs_MissingRef(t *testing.T) {
	var stdout bytes.Buffer
	out := NewOutput(&stdout, &bytes.Buffer{}, false)

	err := runHerdrPlugin(context.Background(), []string{"logs"}, out)
	if err == nil {
		t.Fatal("logs: expected a usage error for a missing ref, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("logs: error is not *UsageError: %T: %v", err, err)
	}
}

func TestHerdrPlugin_unknownSubcommand(t *testing.T) {
	out := NewOutput(&bytes.Buffer{}, &bytes.Buffer{}, false)
	err := runHerdrPlugin(context.Background(), []string{"nonexistent"}, out)
	if err == nil {
		t.Fatal("expected error for unknown subcommand, got nil")
	}
	// The message must be actionable: name the binary and say it is stale.
	// MUTATION PROOF: drop the staleness hint from the default case →
	// strings.Contains(msg, "stale") fails → RED.
	ue, ok := err.(*UsageError)
	if !ok {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "stale") {
		t.Errorf("error message should mention staleness; got: %q", ue.Msg)
	}
	if !strings.Contains(ue.Msg, "install-default-shell") {
		t.Errorf("error message should give the rebuild command; got: %q", ue.Msg)
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
	// The profile must be set even though nothing here seeds credentials: it is
	// what puts AgentName on the sandbox record, and without it the record
	// cannot be told apart from a plain sandbox on a later start.
	if opts.AgentProfile.Name != cred.ClaudeCodeProfileName {
		t.Errorf("agentEgress=true: AgentProfile.Name = %q, want %q", opts.AgentProfile.Name, cred.ClaudeCodeProfileName)
	}
	// The allowlist must come from that same profile, so the hosts the sandbox
	// may reach and the agent it is recorded as running cannot drift apart.
	want := opts.AgentProfile.Egress()
	if !slices.Equal(opts.AllowedHosts, want) {
		t.Errorf("AllowedHosts = %v, want the profile's egress %v", opts.AllowedHosts, want)
	}
}

// The plain launch path must record no agent. An empty AgentName is what marks
// a sandbox as having no credential seed; defaulting it here would claim every
// launched sandbox is an agent sandbox.
func TestBuildLaunchBootOpts_noEgress_recordsNoAgent(t *testing.T) {
	opts := buildLaunchBootOpts("myimage:latest", t.TempDir(), false)
	if opts.AgentProfile.Name != "" {
		t.Errorf("agentEgress=false: AgentProfile.Name = %q, want empty", opts.AgentProfile.Name)
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

// ── herdrReadMountSpec unit tests ─────────────────────────────────────────────

func TestHerdrReadMountSpec_Empty_NoFlags(t *testing.T) {
	// Blank answer must produce nil flags — empty = no mount.
	flags, err := herdrReadMountSpec(bufio.NewScanner(strings.NewReader("\n")))
	if err != nil {
		t.Fatalf("unexpected error for blank: %v", err)
	}
	if len(flags) != 0 {
		t.Errorf("blank mount spec: want no flags, got %v", flags)
	}
}

func TestHerdrReadMountSpec_Valid_ReturnsMountFlag(t *testing.T) {
	flags, err := herdrReadMountSpec(bufio.NewScanner(strings.NewReader("/tmp:/work\n")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(flags) != 2 || flags[0] != "--mount" || flags[1] != "/tmp:/work" {
		t.Errorf("want [--mount /tmp:/work], got %v", flags)
	}
}

func TestHerdrReadMountSpec_ValidRO(t *testing.T) {
	flags, err := herdrReadMountSpec(bufio.NewScanner(strings.NewReader("/tmp:/work:ro\n")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(flags) != 2 || flags[0] != "--mount" || flags[1] != "/tmp:/work:ro" {
		t.Errorf("want [--mount /tmp:/work:ro], got %v", flags)
	}
}

func TestHerdrReadMountSpec_Invalid_Error(t *testing.T) {
	// A spec with no colon is rejected before any VM is created.
	_, err := herdrReadMountSpec(bufio.NewScanner(strings.NewReader("notaspec\n")))
	if err == nil {
		t.Fatal("invalid spec: expected error, got nil")
	}
}

func TestHerdrReadMountSpec_NonExistentHost_Error(t *testing.T) {
	_, err := herdrReadMountSpec(bufio.NewScanner(strings.NewReader("/nonexistent-nexus3-test-path:/work\n")))
	if err == nil {
		t.Fatal("non-existent host path: expected error, got nil")
	}
}

// ── Wiring mutation tests ─────────────────────────────────────────────────────
//
// These tests prove the wiring from herdrReadMountSpec to the sandbox-create
// exec call. Mutation: delete "args = append(args, mountArgs...)" from
// herdrPluginCreate → TestHerdrPluginCreate_MountWired fails with
// "--mount not forwarded to sandbox create".

func TestHerdrPluginCreate_MountWired(t *testing.T) {
	// Point resolveKernelPath at a file that exists.
	fakeKernel := t.TempDir() + "/vmlinux-x86_64"
	if err := os.WriteFile(fakeKernel, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", fakeKernel)

	// Intercept the subprocess and capture args; return non-zero exit so we
	// stop before herdrPluginSpaceCreate (which needs a real svc).
	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "false")
	}
	defer func() { herdrExecCommandContext = old }()

	// stdin order: image, project, name, github-repo (blank→no-gh), mount-spec
	input := "sha256:abc\nteam\nbox\n\n/tmp:/work\n"
	var w strings.Builder
	_ = herdrPluginCreate(context.Background(), strings.NewReader(input), &w, nil, t.TempDir())

	// The wiring line "args = append(args, mountArgs...)" must forward --mount.
	if !slices.Contains(capturedArgs, "--mount") {
		t.Fatalf("--mount not forwarded to sandbox create; args: %v", capturedArgs)
	}
	idx := slices.Index(capturedArgs, "--mount")
	if idx+1 >= len(capturedArgs) || capturedArgs[idx+1] != "/tmp:/work" {
		t.Fatalf("--mount value wrong in sandbox create args: %v", capturedArgs)
	}
}

func TestHerdrPluginCreate_MountEmpty_NoFlag(t *testing.T) {
	// Blank mount answer → sandbox create must NOT receive --mount.
	fakeKernel := t.TempDir() + "/vmlinux-x86_64"
	if err := os.WriteFile(fakeKernel, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", fakeKernel)

	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "false")
	}
	defer func() { herdrExecCommandContext = old }()

	// stdin order: image, project, name, github-repo, mount (blank)
	input := "sha256:abc\nteam\nbox\n\n\n"
	var w strings.Builder
	_ = herdrPluginCreate(context.Background(), strings.NewReader(input), &w, nil, t.TempDir())

	if slices.Contains(capturedArgs, "--mount") {
		t.Fatalf("--mount must not appear when mount answer is blank; args: %v", capturedArgs)
	}
}

func TestHerdrPluginCreate_MountInvalid_ErrorBeforeExec(t *testing.T) {
	// Invalid mount spec → error returned before any exec call.
	fakeKernel := t.TempDir() + "/vmlinux-x86_64"
	if err := os.WriteFile(fakeKernel, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", fakeKernel)

	execCalled := false
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "false")
	}
	defer func() { herdrExecCommandContext = old }()

	// stdin: image, project, name, github-repo, invalid mount spec (no host path colon)
	input := "sha256:abc\nteam\nbox\n\nbadspec\n"
	var w strings.Builder
	err := herdrPluginCreate(context.Background(), strings.NewReader(input), &w, nil, t.TempDir())

	if err == nil {
		t.Fatal("invalid mount spec: expected error, got nil")
	}
	if execCalled {
		t.Fatal("exec must not be called when mount spec is invalid; no half-created sandbox")
	}
}

// --- herdrPluginNewTab tests ---

// fakeNewTabGetter is a non-nil herdrAdoptGetter for the new-tab tests.
//
// Passing nil here would "work": under a mutation that always takes the
// guest-pane branch, the nil svc panics and the test goes red. But it would go
// red by CRASHING, not by its own predicate — and a future nil-safety fix would
// silently disarm it. A real fake lets the assertions below do the deciding.
type fakeNewTabGetter struct{}

func (fakeNewTabGetter) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}

// TestHerdrNewTab_BindingFound asserts that when a nexus3 binding exists for
// the focused workspace ID, herdrPluginNewTab opens a guest-shell pane via
// `herdr plugin pane open` and does NOT call `herdr tab create`.
func TestHerdrNewTab_BindingFound(t *testing.T) {
	storeRoot := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:test-sb",
		HerdrWorkspaceID: "wAA",
		SandboxHandle:    "test/sb",
		SandboxID:        "sb-test",
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		// Return true so the pane-open call succeeds and the function returns nil.
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")

	svc := newTestHerdrService(t)
	var w strings.Builder
	err := herdrPluginNewTab(context.Background(), "wAA", storeRoot, svc, &w)
	if err != nil {
		t.Fatalf("binding found: unexpected error: %v", err)
	}
	// Must have called `herdr plugin pane open` (guest pane), NOT `herdr tab create`.
	if !slices.Contains(capturedArgs, "pane") {
		t.Errorf("binding found: expected `plugin pane open` call; got args: %v", capturedArgs)
	}
	if slices.Contains(capturedArgs, "tab") {
		t.Errorf("binding found: must not call `herdr tab create`; got args: %v", capturedArgs)
	}
}

// TestHerdrNewTab_NoBinding asserts that when no nexus3 binding exists for the
// workspace ID, herdrPluginNewTab calls `herdr tab create --workspace <id>
// --focus` and does NOT open a guest pane.
func TestHerdrNewTab_NoBinding(t *testing.T) {
	storeRoot := t.TempDir() // empty store: no bindings

	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")

	var w strings.Builder
	err := herdrPluginNewTab(context.Background(), "wBB", storeRoot, fakeNewTabGetter{}, &w)
	if err != nil {
		t.Fatalf("no binding: unexpected error: %v", err)
	}
	// Must have called `herdr tab create --workspace wBB --focus`.
	if !slices.Contains(capturedArgs, "tab") {
		t.Errorf("no binding: expected `herdr tab create`; got args: %v", capturedArgs)
	}
	if !slices.Contains(capturedArgs, "wBB") {
		t.Errorf("no binding: workspace ID missing in tab create args: %v", capturedArgs)
	}
	if slices.Contains(capturedArgs, "pane") {
		t.Errorf("no binding: must not call `herdr plugin pane open`; got args: %v", capturedArgs)
	}
}

// TestHerdrNewTab_LookupError asserts that a transient store error (not
// ErrHerdrSpaceNotFound) triggers the host-tab fallback with no error returned.
// The operator pressed new-tab and must get a tab; erroring out is not an option.
func TestHerdrNewTab_LookupError(t *testing.T) {
	// Write an invalid bindings JSON so HerdrSpaceList returns a parse error.
	storeRoot := t.TempDir()
	bindingsPath := filepath.Join(storeRoot, "herdr-space-bindings.json")
	if err := os.WriteFile(bindingsPath, []byte("{{{invalid json"), 0600); err != nil {
		t.Fatal(err)
	}

	var capturedArgs []string
	old := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "true")
	}
	defer func() { herdrExecCommandContext = old }()

	t.Setenv("HERDR_BIN_PATH", "/fake/herdr")

	var w strings.Builder
	// Must return nil (degraded to host tab, not an error).
	err := herdrPluginNewTab(context.Background(), "wCC", storeRoot, fakeNewTabGetter{}, &w)
	if err != nil {
		t.Fatalf("lookup error: expected nil (host-tab fallback), got: %v", err)
	}
	// Must have fallen through to `herdr tab create`.
	if !slices.Contains(capturedArgs, "tab") {
		t.Errorf("lookup error: expected host-tab fallback; got args: %v", capturedArgs)
	}
}

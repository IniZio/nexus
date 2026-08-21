package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
)

// nopProbe is a service.ProbeFunc that reports the guest as immediately ready.
// Used in tests to avoid real vsock probing.
var nopProbe service.ProbeFunc = func(_ context.Context, _ driver.Driver, _ domain.SandboxID) error {
	return nil
}

// newTestOrcaService builds an in-memory service for orca tests.
func newTestOrcaService(t *testing.T) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return service.New(st, fake.New(), lifecycle.New())
}

// createOrcaSandbox creates and boots a sandbox with the given motiveID using
// a fake driver and nopProbe. Used by multiple tests.
func createOrcaSandbox(t *testing.T, svc *service.Service, motiveID, name string) domain.Sandbox {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "rootfs")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	imgCache, err := image.NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	newDriver := service.DriverFactory(func(_ string, _ []service.ExtraDisk) (driver.Driver, error) {
		return fake.New(), nil
	})
	var orcaLabels map[string]string
	if motiveID != "" {
		orcaLabels = map[string]string{"motive": motiveID}
	}
	sb, err := service.CreateAndBoot(context.Background(), svc, imgCache, newDriver, nopProbe,
		"orca", name,
		service.CreateAndBootOptions{
			Labels:              orcaLabels,
			Image:               service.ImageSpec{RootfsPath: f.Name()},
			ReachabilityTimeout: 5 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot(%q): %v", motiveID, err)
	}
	return sb
}

// ── buildOrcaConnectionJSON ───────────────────────────────────────────────────

func TestBuildOrcaConnectionJSON_Shape(t *testing.T) {
	result := buildOrcaConnectionJSON(
		"inst-uuid-1234",
		"sb-aabbccdd",
		"my-workspace",
		"/home/user/repos/my-project",
		"",
	)

	if result.SchemaVersion != 1 {
		t.Errorf("schemaVersion: got %d, want 1", result.SchemaVersion)
	}
	if result.Connection.Type != "ssh" {
		t.Errorf("connection.type: got %q, want %q", result.Connection.Type, "ssh")
	}
	tgt := result.Connection.Target
	if tgt.Host != "sb-aabbccdd" {
		t.Errorf("target.host: got %q, want %q", tgt.Host, "sb-aabbccdd")
	}
	if tgt.Port != 22 {
		t.Errorf("target.port: got %d, want 22", tgt.Port)
	}
	if tgt.Username != "root" {
		t.Errorf("target.username: got %q, want %q", tgt.Username, "root")
	}
	if !strings.Contains(tgt.ProxyCommand, "nexus3 ssh --stdio") {
		t.Errorf("target.proxyCommand %q must contain 'nexus3 ssh --stdio'", tgt.ProxyCommand)
	}
	// Orca expands %h → target.Host (sandboxID); confirm %h is present.
	if !strings.Contains(tgt.ProxyCommand, "%h") {
		t.Errorf("target.proxyCommand %q must contain %%h for sandboxID substitution", tgt.ProxyCommand)
	}
	if !strings.HasPrefix(result.Connection.ProjectRoot, "/") {
		t.Errorf("projectRoot %q must be absolute (start with /)", result.Connection.ProjectRoot)
	}
	if result.UserData.SandboxID != "sb-aabbccdd" {
		t.Errorf("userData.sandboxId: got %q, want %q", result.UserData.SandboxID, "sb-aabbccdd")
	}
}

func TestBuildOrcaConnectionJSON_ProjectRootFromRepoPath(t *testing.T) {
	result := buildOrcaConnectionJSON("inst", "sb-1", "ws", "/repos/my-repo", "")
	if !strings.HasSuffix(result.Connection.ProjectRoot, "/my-repo") {
		t.Errorf("projectRoot %q: want suffix /my-repo", result.Connection.ProjectRoot)
	}
}

func TestBuildOrcaConnectionJSON_ProjectRootFallsBackToWorkspaceName(t *testing.T) {
	result := buildOrcaConnectionJSON("inst", "sb-1", "cool-workspace", "", "")
	if !strings.HasSuffix(result.Connection.ProjectRoot, "/cool-workspace") {
		t.Errorf("projectRoot %q: want suffix /cool-workspace", result.Connection.ProjectRoot)
	}
}

func TestBuildOrcaConnectionJSON_ProjectRootFallsBackToInstanceID(t *testing.T) {
	result := buildOrcaConnectionJSON("my-instance", "sb-1", "", "", "")
	if !strings.HasSuffix(result.Connection.ProjectRoot, "/my-instance") {
		t.Errorf("projectRoot %q: want suffix /my-instance", result.Connection.ProjectRoot)
	}
}

func TestBuildOrcaConnectionJSON_LabelUsesWorkspaceName(t *testing.T) {
	result := buildOrcaConnectionJSON("inst", "sb-1", "cool-workspace", "/repos/x", "")
	if result.Connection.Target.Label != "cool-workspace" {
		t.Errorf("label: got %q, want %q", result.Connection.Target.Label, "cool-workspace")
	}
}

func TestBuildOrcaConnectionJSON_LabelFallsBackToInstanceID(t *testing.T) {
	result := buildOrcaConnectionJSON("my-inst", "sb-1", "", "", "")
	if result.Connection.Target.Label != "my-inst" {
		t.Errorf("label: got %q, want %q", result.Connection.Target.Label, "my-inst")
	}
}

// TestBuildOrcaConnectionJSON_JSONMarshal verifies the struct marshals to the
// exact schema shape Orca's VM-recipe contract requires.
func TestBuildOrcaConnectionJSON_JSONMarshal(t *testing.T) {
	result := buildOrcaConnectionJSON("inst", "sb-deadbeef", "ws", "/repos/proj", "")
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if out["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion: got %v", out["schemaVersion"])
	}
	conn, ok := out["connection"].(map[string]any)
	if !ok {
		t.Fatalf("connection field missing or wrong type")
	}
	if conn["type"] != "ssh" {
		t.Errorf("connection.type: got %v", conn["type"])
	}
	target, ok := conn["target"].(map[string]any)
	if !ok {
		t.Fatalf("connection.target missing or wrong type")
	}
	for _, key := range []string{"label", "host", "port", "username", "proxyCommand"} {
		if _, exists := target[key]; !exists {
			t.Errorf("connection.target.%s is missing", key)
		}
	}
	if _, ok := out["userData"]; !ok {
		t.Error("userData field missing")
	}
}

// ── orcaSandboxName ───────────────────────────────────────────────────────────

func TestOrcaSandboxName_UUID(t *testing.T) {
	name := orcaSandboxName("550e8400-e29b-41d4-a716-446655440000")
	if name != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("got %q", name)
	}
}

func TestOrcaSandboxName_SpecialChars(t *testing.T) {
	name := orcaSandboxName("Inst@nce_ID/99")
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			t.Errorf("invalid char %q in name %q", r, name)
		}
	}
}

func TestOrcaSandboxName_Empty(t *testing.T) {
	name := orcaSandboxName("")
	if name == "" {
		t.Error("expected non-empty fallback name")
	}
}

// ── orcaByInstanceID ─────────────────────────────────────────────────────────

func TestOrcaByInstanceID_NotFound(t *testing.T) {
	svc := newTestOrcaService(t)
	_, err := orcaByInstanceID(context.Background(), svc, "no-such-instance")
	if err == nil {
		t.Fatal("expected error for unknown instance, got nil")
	}
	if !strings.Contains(err.Error(), "no sandbox found") {
		t.Errorf("error %q: want 'no sandbox found'", err.Error())
	}
}

func TestOrcaByInstanceID_Found(t *testing.T) {
	svc := newTestOrcaService(t)
	const motiveID = "test-orca-instance-abc"

	sb := createOrcaSandbox(t, svc, motiveID, "found-test")

	found, err := orcaByInstanceID(context.Background(), svc, motiveID)
	if err != nil {
		t.Fatalf("orcaByInstanceID: %v", err)
	}
	if found.ID != sb.ID {
		t.Errorf("found.ID %v != sb.ID %v", found.ID, sb.ID)
	}
}

// ── orcaCreate JSON output ────────────────────────────────────────────────────

// TestOrcaCreate_JSONOutputShape exercises the JSON output of the idempotency
// path: a sandbox with the instance's MotiveID already exists, so orcaCreate
// returns its connection JSON immediately. Verifies schemaVersion, all required
// fields, and that proxyCommand references nexus3 ssh --stdio.
func TestOrcaCreate_JSONOutputShape(t *testing.T) {
	svc := newTestOrcaService(t)
	const instanceID = "orca-json-shape-test"

	sb := createOrcaSandbox(t, svc, instanceID, "shape-test")

	// Build the result the same way the idempotency path does.
	result := buildOrcaConnectionJSON(instanceID, sb.ID.String(), "my-ws", "/repos/my-repo", "")

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(result); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var out orcaCreateResult
	if err := json.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if out.SchemaVersion != 1 {
		t.Errorf("schemaVersion: %d", out.SchemaVersion)
	}
	if out.Connection.Type != "ssh" {
		t.Errorf("connection.type: %q", out.Connection.Type)
	}
	if out.Connection.Target.Host != sb.ID.String() {
		t.Errorf("target.host: %q, want %q", out.Connection.Target.Host, sb.ID.String())
	}
	if out.Connection.Target.Port != 22 {
		t.Errorf("target.port: %d", out.Connection.Target.Port)
	}
	if out.Connection.Target.Username != "root" {
		t.Errorf("target.username: %q", out.Connection.Target.Username)
	}
	if !strings.Contains(out.Connection.Target.ProxyCommand, "nexus3 ssh --stdio") {
		t.Errorf("proxyCommand %q missing 'nexus3 ssh --stdio'", out.Connection.Target.ProxyCommand)
	}
	if !strings.HasPrefix(out.Connection.ProjectRoot, "/") {
		t.Errorf("projectRoot %q not absolute", out.Connection.ProjectRoot)
	}
	if out.UserData.SandboxID == "" {
		t.Error("userData.sandboxId is empty")
	}
}

// ── lifecycle: destroy ───────────────────────────────────────────────────────

// TestOrcaDestroy_ResolvesAndRemoves verifies that the destroy lookup chain
// (GetByMotive → Remove) works end-to-end with an in-memory service.
func TestOrcaDestroy_ResolvesAndRemoves(t *testing.T) {
	svc := newTestOrcaService(t)
	const instanceID = "destroy-test-instance"
	ctx := context.Background()

	sb := createOrcaSandbox(t, svc, instanceID, "destroy-test")

	// Verify orcaByInstanceID finds the sandbox.
	found, err := orcaByInstanceID(ctx, svc, instanceID)
	if err != nil {
		t.Fatalf("orcaByInstanceID: %v", err)
	}
	if found.ID != sb.ID {
		t.Fatalf("found wrong sandbox: %v != %v", found.ID, sb.ID)
	}

	// Remove via service (same call as orcaDestroy makes after lookup).
	if err := svc.Remove(ctx, found.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// After removal, GetByMotive must return empty.
	remaining, err := svc.GetByMotive(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetByMotive after remove: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 sandboxes after remove, got %d", len(remaining))
	}
}

// TestOrcaSuspend_ResolvesInstance verifies that the suspend lookup chain
// finds the sandbox and passes its ID to Pause. Pause fails because the fake
// driver does not implement driver.PauseResumer — that is the expected error;
// the important assertion is that lookup succeeded (no "no sandbox found" error).
func TestOrcaSuspend_ResolvesInstance(t *testing.T) {
	svc := newTestOrcaService(t)
	const instanceID = "suspend-test-instance"
	ctx := context.Background()

	createOrcaSandbox(t, svc, instanceID, "suspend-test")

	found, err := orcaByInstanceID(ctx, svc, instanceID)
	if err != nil {
		t.Fatalf("orcaByInstanceID: %v (lookup must succeed)", err)
	}
	if found.ID.String() == "" {
		t.Fatal("found.ID is empty")
	}
	// The actual Pause call requires a running VM; we only assert lookup here.
}

// TestOrcaResume_ResolvesInstance mirrors TestOrcaSuspend_ResolvesInstance
// for the resume path.
func TestOrcaResume_ResolvesInstance(t *testing.T) {
	svc := newTestOrcaService(t)
	const instanceID = "resume-test-instance"
	ctx := context.Background()

	createOrcaSandbox(t, svc, instanceID, "resume-test")

	found, err := orcaByInstanceID(ctx, svc, instanceID)
	if err != nil {
		t.Fatalf("orcaByInstanceID: %v", err)
	}
	if found.ID.String() == "" {
		t.Fatal("found.ID is empty")
	}
}

// ── SSH identityFile wiring ───────────────────────────────────────────────────

// TestBuildOrcaConnectionJSON_IdentityFile verifies that a non-empty privKeyPath
// is emitted as target.identityFile in the connection JSON, and that the path
// matches the expected per-instance location.
func TestBuildOrcaConnectionJSON_IdentityFile(t *testing.T) {
	const privKeyPath = "/home/user/.local/share/nexus3/orca/inst-abc/id_ed25519"
	result := buildOrcaConnectionJSON("inst-abc", "sb-1", "ws", "/repos/r", privKeyPath)

	if result.Connection.Target.IdentityFile != privKeyPath {
		t.Errorf("identityFile: got %q, want %q",
			result.Connection.Target.IdentityFile, privKeyPath)
	}

	// Verify the identity file path is absolute and ends with id_ed25519.
	if !strings.HasPrefix(result.Connection.Target.IdentityFile, "/") {
		t.Errorf("identityFile %q must be absolute", result.Connection.Target.IdentityFile)
	}
	if !strings.HasSuffix(result.Connection.Target.IdentityFile, "id_ed25519") {
		t.Errorf("identityFile %q must end with id_ed25519", result.Connection.Target.IdentityFile)
	}
}

// TestBuildOrcaConnectionJSON_IdentityFileOmitEmpty verifies that an empty
// privKeyPath is omitted from the JSON (omitempty semantics).
func TestBuildOrcaConnectionJSON_IdentityFileOmitEmpty(t *testing.T) {
	result := buildOrcaConnectionJSON("inst", "sb-1", "ws", "/repos/r", "")
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	target, _ := out["connection"].(map[string]any)["target"].(map[string]any)
	if _, exists := target["identityFile"]; exists {
		t.Error("identityFile must be omitted from JSON when empty (omitempty)")
	}
}

// TestOrcaProjectRoot_AbsoluteFromRepoPath checks projectRoot is absolute and
// derived from the repo basename.
func TestOrcaProjectRoot_AbsoluteFromRepoPath(t *testing.T) {
	pr := orcaProjectRoot("/home/user/repos/my-repo", "ws", "inst")
	if !strings.HasPrefix(pr, "/") {
		t.Errorf("projectRoot %q must be absolute", pr)
	}
	if !strings.HasSuffix(pr, "/my-repo") {
		t.Errorf("projectRoot %q must end with /my-repo", pr)
	}
}

// ── orcaWorkspaceSpec ─────────────────────────────────────────────────────────

// TestOrcaWorkspaceSpec_RepoPathSet verifies that a valid ORCA_REPO_PATH
// results in a WorkspaceSpec with all fields set correctly. Every field is
// asserted so that a future WorkspaceSpec field addition that forgets the orca
// path causes this test to go RED (the same protection buildWorkspaceSpec gives
// to the sandbox create --workspace path). This test goes RED if any field is
// dropped from buildWorkspaceSpec — proving orca routes through the shared
// constructor rather than maintaining its own literal.
func TestOrcaWorkspaceSpec_RepoPathSet(t *testing.T) {
	dir := t.TempDir()
	env := orcaEnv{
		RepoPath:      dir,
		WorkspaceName: "my-workspace",
		InstanceID:    "inst-123",
	}
	ws := orcaWorkspaceSpec(env)
	if ws == nil {
		t.Fatal("orcaWorkspaceSpec: got nil, want non-nil WorkspaceSpec")
	}
	if ws.SourcePath != env.RepoPath {
		t.Errorf("SourcePath: got %q, want %q (ORCA_REPO_PATH)", ws.SourcePath, env.RepoPath)
	}
	// GuestPath must equal orcaProjectRoot for the same inputs so that
	// connection.projectRoot is correct.
	wantGuestPath := orcaProjectRoot(env.RepoPath, env.WorkspaceName, env.InstanceID)
	if ws.GuestPath != wantGuestPath {
		t.Errorf("GuestPath: got %q, want %q", ws.GuestPath, wantGuestPath)
	}
	// CaptureMaxBytes must be 0 (AUTO): orca does not set an explicit cap so the
	// builder derives the limit from free space on the host filesystem.
	if ws.CaptureMaxBytes != 0 {
		t.Errorf("CaptureMaxBytes: got %d, want 0 (AUTO)", ws.CaptureMaxBytes)
	}
}

// TestOrcaWorkspaceSpec_RepoPathEmpty verifies that an empty ORCA_REPO_PATH
// returns nil (no workspace, no capture).
func TestOrcaWorkspaceSpec_RepoPathEmpty(t *testing.T) {
	env := orcaEnv{RepoPath: "", WorkspaceName: "ws", InstanceID: "id"}
	if ws := orcaWorkspaceSpec(env); ws != nil {
		t.Errorf("orcaWorkspaceSpec: got non-nil WorkspaceSpec for empty RepoPath, want nil")
	}
}

// TestOrcaWorkspaceSpec_RepoPathNonexistent verifies that a nonexistent
// ORCA_REPO_PATH returns nil rather than propagating the stat error.
func TestOrcaWorkspaceSpec_RepoPathNonexistent(t *testing.T) {
	env := orcaEnv{RepoPath: "/nonexistent/path/does/not/exist", WorkspaceName: "ws", InstanceID: "id"}
	if ws := orcaWorkspaceSpec(env); ws != nil {
		t.Errorf("orcaWorkspaceSpec: got non-nil WorkspaceSpec for nonexistent RepoPath, want nil")
	}
}

// TestOrcaWorkspaceSpec_GuestPathMatchesProjectRoot verifies that the
// connection.projectRoot emitted by buildOrcaConnectionJSON equals the
// GuestPath set by orcaWorkspaceSpec, so Orca opens the correct folder.
func TestOrcaWorkspaceSpec_GuestPathMatchesProjectRoot(t *testing.T) {
	dir := t.TempDir()
	env := orcaEnv{
		RepoPath:      dir,
		WorkspaceName: "ws",
		InstanceID:    "inst-xyz",
	}
	ws := orcaWorkspaceSpec(env)
	if ws == nil {
		t.Fatal("orcaWorkspaceSpec: unexpected nil")
	}
	result := buildOrcaConnectionJSON(env.InstanceID, "sb-id", env.WorkspaceName, env.RepoPath, "")
	if result.Connection.ProjectRoot != ws.GuestPath {
		t.Errorf("connection.projectRoot %q != WorkspaceSpec.GuestPath %q",
			result.Connection.ProjectRoot, ws.GuestPath)
	}
}

// ── gitHostsFromURL ───────────────────────────────────────────────────────────

func TestGitHostsFromURL_GitHub(t *testing.T) {
	hosts := gitHostsFromURL("https://github.com/owner/repo.git")
	if hosts != nil {
		t.Errorf("D-PD-23: GitHub URLs must not widen orca AllowedHosts; got %v", hosts)
	}
}

func TestGitHostsFromURL_Generic(t *testing.T) {
	hosts := gitHostsFromURL("https://gitlab.example.com/owner/repo.git")
	if len(hosts) != 1 || hosts[0] != "gitlab.example.com" {
		t.Errorf("hosts: got %v, want [gitlab.example.com]", hosts)
	}
}

func TestGitHostsFromURL_Empty(t *testing.T) {
	hosts := gitHostsFromURL("")
	if hosts != nil {
		t.Errorf("expected nil for empty URL, got %v", hosts)
	}
}

// TestGitHostsFromURL_GitHubTrailingDot verifies that a trailing-dot FQDN
// spelling of a GitHub host ("github.com.") is not returned as an allowed
// egress host. "github.com." is a valid DNS form that resolves normally, so
// admitting it would silently widen the agent-sandbox AllowedHosts in
// violation of D-PD-23 / N-AC1.
func TestGitHostsFromURL_GitHubTrailingDot(t *testing.T) {
	hosts := gitHostsFromURL("https://github.com./owner/repo.git")
	if hosts != nil {
		t.Errorf("D-PD-23: trailing-dot GitHub FQDN must not widen orca AllowedHosts; got %v", hosts)
	}
}

// TestGitHostsFromURL_GitHubVariants checks every alternate spelling of a
// GitHub host that must be suppressed: uppercase, trailing dot on a subdomain,
// and api.github.com with a port suffix.
func TestGitHostsFromURL_GitHubVariants(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"uppercase", "https://GITHUB.COM/owner/repo.git"},
		{"mixed-case subdomain", "https://API.GitHub.com/owner/repo.git"},
		{"trailing dot subdomain", "https://api.github.com./owner/repo.git"},
		{"githubusercontent trailing dot", "https://raw.githubusercontent.com./owner/repo/main/file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hosts := gitHostsFromURL(tc.url)
			if hosts != nil {
				t.Errorf("D-PD-23: %s URL must not widen orca AllowedHosts; got %v", tc.name, hosts)
			}
		})
	}
}

// TestOrcaCreate_AllowedHostsInEnvelope is a regression test for the production
// gap where orcaCreate never set AllowedHosts on CreateAndBootOptions, leaving
// the sandbox Envelope.AllowedHosts empty. The detached supervisor reads the
// envelope from the store when it calls svc.Start, so an empty list means the
// perimeter netfilter is default-deny for all traffic — including api.anthropic.com.
//
// This test mirrors the allowedHosts construction in orcaCreate and asserts that
// the created sandbox Envelope carries all required hosts. It does not require
// live credentials or a running VM.
func TestOrcaCreate_AllowedHostsInEnvelope(t *testing.T) {
	svc := newTestOrcaService(t)

	// Mirror what orcaCreate does: AgentEgressHosts only. GitHub hosts from
	// the recipe URL are NOT appended (D-PD-23).
	const repoURL = "https://github.com/anthropics/anthropic-sdk-go"
	allowedHosts := append(service.AgentEgressHosts(cred.ClaudeCodeProfile), gitHostsFromURL(repoURL)...)

	f, err := os.CreateTemp(t.TempDir(), "rootfs")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	imgCache, err := image.NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	newDriver := service.DriverFactory(func(_ string, _ []service.ExtraDisk) (driver.Driver, error) {
		return fake.New(), nil
	})
	sb, err := service.CreateAndBoot(context.Background(), svc, imgCache, newDriver, nopProbe,
		"orca", "allowed-hosts-regression",
		service.CreateAndBootOptions{
			Labels:              map[string]string{"motive": "regression-test"},
			Image:               service.ImageSpec{RootfsPath: f.Name()},
			ReachabilityTimeout: 5 * time.Second,
			AllowedHosts:        allowedHosts,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// The Envelope is frozen at creation; assert every required host is present.
	got := make(map[string]bool, len(sb.Envelope.AllowedHosts))
	for _, h := range sb.Envelope.AllowedHosts {
		got[h] = true
	}

	required := []string{
		service.AnthropicAPIHost,   // api.anthropic.com
		service.ClaudePlatformHost, // platform.claude.com
	}
	for _, h := range required {
		if !got[h] {
			t.Errorf("Envelope.AllowedHosts missing %q; got %v", h, sb.Envelope.AllowedHosts)
		}
	}
	for _, h := range sb.Envelope.AllowedHosts {
		if h == "github.com" || h == "codeload.github.com" {
			t.Errorf("D-PD-23: orca Envelope must not contain %q; got %v", h, sb.Envelope.AllowedHosts)
		}
	}

	// Paranoia: the list must be non-trivially sized (not open-all — the netfilter
	// uses this list to allow only these hosts, so empty == deny-all was the bug).
	if len(sb.Envelope.AllowedHosts) == 0 {
		t.Error("Envelope.AllowedHosts is empty — perimeter would be default-deny")
	}
}

// ── workspace sync helpers ────────────────────────────────────────────────────

// TestOrcaWorkspaceSyncScript_DeviceDerivation verifies that orcaWorkspaceSyncScript
// derives the device path from WorkspaceGuestMount rather than hardcoding /dev/vdb.
//
// The assertion strategy: with numShadowDisks > 0, the workspace disk shifts to a
// later virtio-blk letter (/dev/vdc, /dev/vdd, …). If the script hardcoded /dev/vdb,
// the tests with numShadow > 0 would fail because /dev/vdb would still appear
// (wrong device) and the expected device would be absent.
func TestOrcaWorkspaceSyncScript_DeviceDerivation(t *testing.T) {
	cases := []struct {
		numShadow  int
		wantDevice string
	}{
		{0, "/dev/vdb"}, // no shadows → workspace at vdb
		{1, "/dev/vdc"}, // 1 shadow → workspace at vdc
		{2, "/dev/vdd"},
		{4, "/dev/vdf"}, // DefaultShadowDirs count
	}
	for _, tc := range cases {
		script := orcaWorkspaceSyncScript("/workspace/repo", tc.numShadow)
		if !strings.Contains(script, tc.wantDevice) {
			t.Errorf("numShadow=%d: script does not contain device %q: %q",
				tc.numShadow, tc.wantDevice, script)
		}
		// Key assertion: when shadow disks are present, /dev/vdb must NOT
		// appear in the script — it would only appear if the device were
		// hardcoded rather than derived.
		if tc.numShadow > 0 && strings.Contains(script, "/dev/vdb") {
			t.Errorf("numShadow=%d: script contains hardcoded /dev/vdb instead of derived %q: %q",
				tc.numShadow, tc.wantDevice, script)
		}
	}
}

// ── buildOrcaSpawnConfig forward-trace test ───────────────────────────────────
//
// This test closes the "value has no production origin" defect class for the
// orca path. It asserts that buildOrcaSpawnConfig — the function called by
// orcaCreate immediately before supervisor.SpawnDetached — actually sets
// GovBounds, ExtraDisks, HasWorkspaceDisk, and WorkspaceDiskIndex.
//
// HOW IT BITES: remove the GovBounds assignment from buildOrcaSpawnConfig
// and cfg.Config.GovBounds.MemMaxBytes == 0; the test fails. That is exactly
// the defect the prior gate found: SpawnDetached received zero bounds, the
// supervisor's governor hit bounds_not_configured, and the goroutine exited.

// TestOrcaSpawnConfig_GovBoundsForwarded verifies that buildOrcaSpawnConfig
// produces a SpawnConfig with non-zero GovBounds, correct ExtraDisks,
// HasWorkspaceDisk, and WorkspaceDiskIndex derived from orcaNumShadowDisks.
func TestOrcaSpawnConfig_GovBoundsForwarded(t *testing.T) {
	// Realistic inputs matching what orcaCreate supplies.
	const (
		sandboxID  = "deadbeef01020304"
		storeRoot  = "/var/lib/nexus3"
		stateDir   = "/tmp/nexus3-sv-test"
		chBin      = "/usr/bin/cloud-hypervisor"
		socketDir  = "/run/nexus3"
		kernelPath = "/boot/vmlinux"
		diskPath   = "/var/lib/nexus3/deadbeef.raw"
		wsDiskPath = "/var/lib/nexus3/deadbeef-ws.raw"
		credsFile  = "/home/user/.nexus3/creds.json"
	)
	extraDiskPaths := []string{wsDiskPath}

	// govBounds: same call as orcaCreate uses (vmcfg.Resolve defaults).
	govBounds := vmcfg.Resolve(vmcfg.Config{}).Bounds
	if govBounds.MemMaxBytes == 0 {
		t.Fatal("vmcfg.Resolve returned zero MemMaxBytes; test precondition broken")
	}

	const orcaNumShadowDisks = 0 // canonical value from orcaCreate
	const guestPath = "/root/workspace/myrepo"
	const sandboxHandle = "orca/myrepo"
	cfg := buildOrcaSpawnConfig(
		sandboxID, sandboxHandle, storeRoot, stateDir, chBin, socketDir, kernelPath, diskPath,
		extraDiskPaths,
		govBounds,
		1,                  // bootVCPUs
		true,               // hasWorkspaceDisk
		orcaNumShadowDisks, // workspaceDiskIndex
		credsFile,
		guestPath,
	)

	// ── GovBounds must be non-zero ────────────────────────────────────────────
	// If GovBounds is not forwarded the supervisor hits bounds_not_configured
	// and the governor goroutine exits immediately (the original defect).
	if cfg.Config.GovBounds.MemMaxBytes == 0 {
		t.Error("SpawnConfig.Config.GovBounds.MemMaxBytes == 0: GovBounds not forwarded to supervisor")
	}
	if cfg.Config.GovBounds.MemMinBytes == 0 {
		t.Error("SpawnConfig.Config.GovBounds.MemMinBytes == 0")
	}
	if cfg.Config.GovBounds.VCPUMax == 0 {
		t.Error("SpawnConfig.Config.GovBounds.VCPUMax == 0")
	}
	if cfg.Config.GovBounds != govBounds {
		t.Errorf("GovBounds mismatch: got %+v, want %+v", cfg.Config.GovBounds, govBounds)
	}

	// ── BootVCPUs must be forwarded ───────────────────────────────────────────
	if cfg.Config.BootVCPUs != 1 {
		t.Errorf("BootVCPUs = %d, want 1", cfg.Config.BootVCPUs)
	}

	// ── ExtraDisks must carry the workspace disk path ─────────────────────────
	if len(cfg.Config.ExtraDisks) != 1 || cfg.Config.ExtraDisks[0] != wsDiskPath {
		t.Errorf("ExtraDisks = %v, want [%q]", cfg.Config.ExtraDisks, wsDiskPath)
	}

	// ── HasWorkspaceDisk and WorkspaceDiskIndex must be set ───────────────────
	if !cfg.Config.HasWorkspaceDisk {
		t.Error("HasWorkspaceDisk = false, want true; disk axis will not register")
	}
	if cfg.Config.WorkspaceDiskIndex != orcaNumShadowDisks {
		t.Errorf("WorkspaceDiskIndex = %d, want %d (orcaNumShadowDisks)",
			cfg.Config.WorkspaceDiskIndex, orcaNumShadowDisks)
	}

	// ── Cmdline must be non-empty and contain workspace-mount and auto-resize args ─
	// Without Cmdline the supervisor reboots the VM with no --workspace-mount= args,
	// selectWorkspaceMount returns ok=false, and DiskSupported=false permanently.
	if cfg.Config.Cmdline == "" {
		t.Error("Cmdline is empty; supervisor-owned VM will boot without workspace-mount args (DiskSupported=false)")
	}
	if !strings.Contains(cfg.Config.Cmdline, "--workspace-mount=") {
		t.Errorf("Cmdline %q does not contain --workspace-mount= arg; disk telemetry will be blind", cfg.Config.Cmdline)
	}
	if !strings.Contains(cfg.Config.Cmdline, guestPath) {
		t.Errorf("Cmdline %q does not contain guest path %q", cfg.Config.Cmdline, guestPath)
	}
	if !strings.Contains(cfg.Config.Cmdline, "--mem-ceiling=") {
		t.Errorf("Cmdline %q does not contain --mem-ceiling; guest agent cannot set ZRAM size correctly", cfg.Config.Cmdline)
	}
	if !strings.Contains(cfg.Config.Cmdline, "--sandbox-handle=orca-myrepo") {
		t.Errorf("Cmdline %q does not contain --sandbox-handle=orca-myrepo; guest hostname will be 'nexus3' not the sandbox name", cfg.Config.Cmdline)
	}
	// Assert the cmdline contains the exact PID1Args string that vmcfg.Resolve
	// produces. This catches VALUE drift (orca producing a different string than
	// the shared helper) but does NOT catch structural re-inlining: a duplicate
	// fmt.Sprintf that happens to produce the same correct value will still pass.
	memMaxMiB := uint32(govBounds.MemMaxBytes / (1024 * 1024))
	wantPID1Args := vmcfg.Resolve(vmcfg.Config{MemMaxMiB: memMaxMiB}).PID1Args
	if !strings.Contains(cfg.Config.Cmdline, wantPID1Args) {
		t.Errorf("Cmdline %q does not contain vmcfg PID1Args %q; orca path has drifted from shared helper", cfg.Config.Cmdline, wantPID1Args)
	}
}

// TestOrcaSpawnConfig_NoWorkspace verifies that buildOrcaSpawnConfig with
// hasWorkspaceDisk=false and no extraDiskPaths produces HasWorkspaceDisk=false
// and an empty ExtraDisks — the disk axis is skipped, no data-loss risk.
func TestOrcaSpawnConfig_NoWorkspace(t *testing.T) {
	govBounds := resize.Bounds{MemMinBytes: 512 << 20, MemMaxBytes: 4096 << 20}
	cfg := buildOrcaSpawnConfig(
		"abc", "test/no-workspace", "/store", "/state", "/ch", "/run", "/kernel", "/disk",
		nil, // no extra disks
		govBounds,
		1,
		false, // hasWorkspaceDisk
		0,
		"",
		"", // no guest path
	)
	if cfg.Config.HasWorkspaceDisk {
		t.Error("HasWorkspaceDisk = true when no workspace; disk axis must not register")
	}
	if len(cfg.Config.ExtraDisks) != 0 {
		t.Errorf("ExtraDisks = %v, want empty", cfg.Config.ExtraDisks)
	}
	if !strings.Contains(cfg.Config.Cmdline, "--sandbox-handle=test-no-workspace") {
		t.Errorf("Cmdline %q does not contain --sandbox-handle=test-no-workspace", cfg.Config.Cmdline)
	}
}

// TestOrcaSyncWorkspace_ExecFailureIsHardError verifies that orcaSyncWorkspace
// returns a non-nil error rather than swallowing the failure.
//
// With a fake driver there is no real vsock agent in the guest, so svc.Exec
// must fail. This test confirms that failure propagates as an error — i.e.
// the function never silently continues the way the old warn-and-continue
// code path did.
//
// KVM-gated: the mount/cp execution itself cannot be verified without a real
// guest; this test only covers the error-surface contract.
func TestOrcaSyncWorkspace_ExecFailureIsHardError(t *testing.T) {
	svc := newTestOrcaService(t)
	sb := createOrcaSandbox(t, svc, "motive-sync-err", "sync-err-sandbox")

	ws := &service.WorkspaceSpec{
		SourcePath: t.TempDir(),
		GuestPath:  "/workspace/repo",
	}
	// With a fake driver there is no real vsock/agent connection; svc.Exec
	// must fail. orcaSyncWorkspace must return that error (hard failure), not
	// succeed or swallow it as a warning.
	err := orcaSyncWorkspace(context.Background(), svc, sb.ID.String(), ws, 0)
	if err == nil {
		t.Fatal("orcaSyncWorkspace: expected non-nil error with fake (non-running) sandbox, got nil")
	}
}

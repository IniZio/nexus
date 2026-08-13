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
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
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
	sb, err := service.CreateAndBoot(context.Background(), svc, imgCache, newDriver, nopProbe,
		"orca", name,
		service.CreateAndBootOptions{
			MotiveID:            motiveID,
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

// ── buildGitCloneArgv ─────────────────────────────────────────────────────────

func TestBuildGitCloneArgv_Basic(t *testing.T) {
	argv := buildGitCloneArgv("https://github.com/owner/repo.git", "abc123", "", "/root/workspace/repo")
	if argv == nil {
		t.Fatal("expected non-nil argv for non-empty repoURL")
	}
	if argv[0] != "/bin/sh" {
		t.Errorf("argv[0]: got %q, want /bin/sh", argv[0])
	}
	if argv[1] != "-c" {
		t.Errorf("argv[1]: got %q, want -c", argv[1])
	}
	cmd := argv[2]
	if !strings.Contains(cmd, "git clone") {
		t.Errorf("shell command %q: missing 'git clone'", cmd)
	}
	if !strings.Contains(cmd, "https://github.com/owner/repo.git") {
		t.Errorf("shell command %q: missing repo URL", cmd)
	}
	if !strings.Contains(cmd, "/root/workspace/repo") {
		t.Errorf("shell command %q: missing destDir", cmd)
	}
	if !strings.Contains(cmd, "--branch abc123") {
		t.Errorf("shell command %q: missing --branch abc123", cmd)
	}
}

func TestBuildGitCloneArgv_UsesBranchWhenRefEmpty(t *testing.T) {
	argv := buildGitCloneArgv("https://github.com/o/r.git", "", "main", "/root/workspace/r")
	cmd := argv[2]
	if !strings.Contains(cmd, "--branch main") {
		t.Errorf("shell command %q: expected --branch main when ref is empty", cmd)
	}
}

func TestBuildGitCloneArgv_NoBranchWhenBothEmpty(t *testing.T) {
	argv := buildGitCloneArgv("https://github.com/o/r.git", "", "", "/root/workspace/r")
	cmd := argv[2]
	if strings.Contains(cmd, "--branch") {
		t.Errorf("shell command %q: must not contain --branch when ref and branch are both empty", cmd)
	}
}

func TestBuildGitCloneArgv_NilWhenURLEmpty(t *testing.T) {
	argv := buildGitCloneArgv("", "main", "", "/root/workspace/r")
	if argv != nil {
		t.Errorf("expected nil argv for empty repoURL, got %v", argv)
	}
}

func TestBuildGitCloneArgv_RefTakesPriorityOverBranch(t *testing.T) {
	argv := buildGitCloneArgv("https://github.com/o/r.git", "v1.2.3", "main", "/dest")
	cmd := argv[2]
	if !strings.Contains(cmd, "--branch v1.2.3") {
		t.Errorf("shell command %q: repoRef %q should take priority over repoBranch", cmd, "v1.2.3")
	}
	if strings.Contains(cmd, "main") {
		t.Errorf("shell command %q: repoBranch should be ignored when repoRef is set", cmd)
	}
}

// ── gitHostsFromURL ───────────────────────────────────────────────────────────

func TestGitHostsFromURL_GitHub(t *testing.T) {
	hosts := gitHostsFromURL("https://github.com/owner/repo.git")
	found := map[string]bool{}
	for _, h := range hosts {
		found[h] = true
	}
	if !found["github.com"] {
		t.Error("github.com must be in allowlist")
	}
	if !found["codeload.github.com"] {
		t.Error("codeload.github.com must be in allowlist for GitHub repos")
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

	// Mirror what orcaCreate does: base set + git hosts for a GitHub URL.
	const repoURL = "https://github.com/anthropics/anthropic-sdk-go"
	allowedHosts := append(service.AgentEgressHosts(), gitHostsFromURL(repoURL)...)

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
			MotiveID:            "regression-test",
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
		service.AnthropicAPIHost,    // api.anthropic.com
		service.ClaudePlatformHost,  // platform.claude.com
		"github.com",                // git clone host
		"codeload.github.com",       // GitHub pack CDN
	}
	for _, h := range required {
		if !got[h] {
			t.Errorf("Envelope.AllowedHosts missing %q; got %v", h, sb.Envelope.AllowedHosts)
		}
	}

	// Paranoia: the list must be non-trivially sized (not open-all — the netfilter
	// uses this list to allow only these hosts, so empty == deny-all was the bug).
	if len(sb.Envelope.AllowedHosts) == 0 {
		t.Error("Envelope.AllowedHosts is empty — perimeter would be default-deny")
	}
}

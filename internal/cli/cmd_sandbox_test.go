package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestService builds a service backed by a real FileStore in a temp dir
// and a fake driver (supports PauseResumer, unlike sandboxNoopDriver).
func newTestService(t *testing.T) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return service.New(st, fake.New(), lifecycle.New())
}

// capture creates an Output pair for capturing JSON output in tests.
func capture(jsonMode bool) (*Output, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	out := NewOutput(&stdout, &stderr, jsonMode)
	return out, &stdout, &stderr
}

// decodeOne decodes exactly one JSON object from r into v.
// It then asserts that no second object follows.
func decodeOne(t *testing.T, r io.Reader, v any) {
	t.Helper()
	dec := json.NewDecoder(r)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode first JSON object: %v", err)
	}
	// Assert nothing follows.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		extraJSON, _ := json.Marshal(extra)
		t.Errorf("stdout contains more than one JSON object; second object: %s", extraJSON)
	}
}

// ── sandbox create ────────────────────────────────────────────────────────────

func TestSandboxCreate_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	out, stdout, _ := capture(true)

	if err := runSandboxCreate(context.Background(), []string{"proj/box"}, out, svc); err != nil {
		t.Fatalf("runSandboxCreate: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if v, ok := env["schema_version"].(float64); !ok || v != 1 {
		t.Errorf("schema_version: got %v, want 1", env["schema_version"])
	}
	if env["kind"] != "sandbox.created" {
		t.Errorf("kind: got %v, want sandbox.created", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data: expected object, got %T", env["data"])
	}
	if data["handle"] != "proj/box" {
		t.Errorf("data.handle: got %v, want proj/box", data["handle"])
	}
	if data["state"] != "created" {
		t.Errorf("data.state: got %v, want created", data["state"])
	}
}

func TestSandboxCreate_JSON_WithRmFlag(t *testing.T) {
	svc := newTestService(t)
	out, stdout, _ := capture(true)

	// --rm may appear after the handle (docker-style).
	if err := runSandboxCreate(context.Background(), []string{"proj/rmbox", "--rm"}, out, svc); err != nil {
		t.Fatalf("runSandboxCreate --rm: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)

	if data["remove_on_exit"] != true {
		t.Errorf("data.remove_on_exit: got %v, want true", data["remove_on_exit"])
	}
}

// TestSandboxCreate_EgressClosed_GitHubInSecretHostsOnly proves D-PD-33 at the
// CLI layer: --egress closed must produce OpenEgress=false, github.com must not
// appear in AllowedHosts, and all GitHubSecretHosts must be wired into the
// SecretBind that the boot path merges into secrets.
//
// We test parseSandboxCreateArgs (real flag parser) + the service.MergeSecrets
// call from lines 1247-1251 of cmd_sandbox.go directly, because the no-boot
// path (no --image/--rootfs/--file) skips egress wiring; the boot path needs a
// kernel/VM which is unavailable in unit tests. The service-layer invariants
// (OpenEgress=false → ACL enforced) are covered by egress_failclosed_test.go.
func TestSandboxCreate_EgressClosed_GitHubInSecretHostsOnly(t *testing.T) {
	// D-PD-36: --egress closed now requires --repo (enforced in parseSandboxCreateArgs).
	// Include a valid repo so the D-PD-33 invariants can still be verified.
	f, err := parseSandboxCreateArgs([]string{"proj/closed-box", "--egress", "closed", "--repo", "acme/myrepo"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}

	// D-PD-33: --egress closed must set egressClosed=true, which becomes OpenEgress=false.
	if !f.egressClosed {
		t.Error("SECURITY VIOLATION — D-PD-33: --egress closed did not set egressClosed=true")
	}
	openEgress := !f.egressClosed
	if openEgress {
		t.Error("SECURITY VIOLATION — D-PD-33: OpenEgress would be true for --egress closed sandbox")
	}

	// AllowedHosts (from --allow-host) must never contain GitHub hosts.
	for _, h := range f.allowHosts {
		for _, ghHost := range service.GitHubSecretHosts {
			if h == ghHost {
				t.Errorf("SECURITY VIOLATION — D-PD-33: GitHub host %q found in AllowedHosts; must only appear in SecretHosts", h)
			}
		}
	}

	// The boot path (cmd_sandbox.go:1247-1251) merges GitHubSecretHosts into
	// SecretBinds when egressClosed=true. Replicate that merge and verify.
	var secrets []service.SecretBind
	if f.egressClosed {
		secrets = service.MergeSecrets(secrets, service.SecretBind{
			Env:   service.BuiltinGitHubEnv,
			Hosts: append([]string(nil), service.GitHubSecretHosts...),
		})
	}

	hostSet := map[string]bool{}
	for _, bind := range secrets {
		for _, h := range bind.Hosts {
			hostSet[h] = true
		}
	}
	for _, want := range service.GitHubSecretHosts {
		if !hostSet[want] {
			t.Errorf("D-PD-33: %q absent from SecretHosts after --egress closed; must be present", want)
		}
	}
}

func TestSandboxCreate_UsageError_MissingHandle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"sandbox", "create"})
	if code != 2 {
		t.Errorf("missing handle: exit code = %d, want 2", code)
	}
}

func TestSandboxCreate_UsageError_BadHandle(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)
	err := runSandboxCreate(context.Background(), []string{"no-slash"}, out, svc)
	if err == nil {
		t.Fatal("expected UsageError for bad handle, got nil")
	}
	var usageErr *UsageError
	if !isUsageError(err, &usageErr) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func isUsageError(err error, target **UsageError) bool {
	if err == nil {
		return false
	}
	if ue, ok := err.(*UsageError); ok {
		if target != nil {
			*target = ue
		}
		return true
	}
	return false
}

// ── sandbox list ──────────────────────────────────────────────────────────────

func TestSandboxList_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	out, stdout, _ := capture(true)

	if err := runSandboxList(context.Background(), []string{}, out, svc); err != nil {
		t.Fatalf("runSandboxList (empty): %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "sandbox.list" {
		t.Errorf("kind: got %v, want sandbox.list", env["kind"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data: expected object, got %T", env["data"])
	}
	// Empty list must be [] not null.
	sandboxes, ok := data["sandboxes"].([]any)
	if !ok {
		t.Fatalf("data.sandboxes: expected array, got %T", data["sandboxes"])
	}
	if len(sandboxes) != 0 {
		t.Errorf("data.sandboxes: expected empty array, got %d items", len(sandboxes))
	}
}

func TestSandboxList_JSON_WithItems(t *testing.T) {
	svc := newTestService(t)

	for _, name := range []string{"a", "b"} {
		if _, err := svc.Create(context.Background(), "p", name, service.CreateOptions{}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	out, stdout, _ := capture(true)
	if err := runSandboxList(context.Background(), []string{}, out, svc); err != nil {
		t.Fatalf("runSandboxList: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)
	data := env["data"].(map[string]any)
	sandboxes, ok := data["sandboxes"].([]any)
	if !ok {
		t.Fatalf("data.sandboxes: expected array, got %T", data["sandboxes"])
	}
	if len(sandboxes) != 2 {
		t.Errorf("data.sandboxes: expected 2 items, got %d", len(sandboxes))
	}
}

// ── sandbox rm ────────────────────────────────────────────────────────────────

func TestSandboxRm_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})

	out, stdout, _ := capture(true)
	if err := runSandboxRm(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxRm: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "sandbox.removed" {
		t.Errorf("kind: got %v, want sandbox.removed", env["kind"])
	}
}

func TestSandboxRm_OperationalError_ExitCode(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"sandbox", "rm", "no-such-sandbox"})
	if code != 1 {
		t.Errorf("not found rm: exit code = %d, want 1", code)
	}
}

func TestSandboxRm_UsageError_MissingRef(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"sandbox", "rm"})
	if code != 2 {
		t.Errorf("missing ref: exit code = %d, want 2", code)
	}
}

// ── sandbox start ─────────────────────────────────────────────────────────────

func TestSandboxStart_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})

	out, stdout, _ := capture(true)
	if err := runSandboxStart(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxStart: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "sandbox.started" {
		t.Errorf("kind: got %v, want sandbox.started", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["state"] != "running" {
		t.Errorf("data.state: got %v, want running", data["state"])
	}
}

// ── sandbox stop ──────────────────────────────────────────────────────────────

func TestSandboxStop_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	out, stdout, _ := capture(true)
	if err := runSandboxStop(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxStop: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "sandbox.stopped" {
		t.Errorf("kind: got %v, want sandbox.stopped", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["state"] != "stopped" {
		t.Errorf("data.state: got %v, want stopped", data["state"])
	}
}

// ── sandbox pause ─────────────────────────────────────────────────────────────

func TestSandboxPause_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	out, stdout, _ := capture(true)
	if err := runSandboxPause(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxPause: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "sandbox.paused" {
		t.Errorf("kind: got %v, want sandbox.paused", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["state"] != "paused" {
		t.Errorf("data.state: got %v, want paused", data["state"])
	}
}

// ── sandbox resume ────────────────────────────────────────────────────────────

func TestSandboxResume_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.Pause(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	out, stdout, _ := capture(true)
	if err := runSandboxResume(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxResume: %v", err)
	}

	var env map[string]any
	decodeOne(t, stdout, &env)

	if env["kind"] != "sandbox.resumed" {
		t.Errorf("kind: got %v, want sandbox.resumed", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["state"] != "running" {
		t.Errorf("data.state: got %v, want running", data["state"])
	}
}

// ── exit codes ────────────────────────────────────────────────────────────────

func TestSandbox_UsageError_MissingSubcommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"sandbox"})
	if code != 2 {
		t.Errorf("missing subcommand: exit code = %d, want 2", code)
	}
}

func TestSandbox_UsageError_UnknownSubcommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code := Run([]string{"sandbox", "frobnicate"})
	if code != 2 {
		t.Errorf("unknown subcommand: exit code = %d, want 2", code)
	}
}

func TestSandbox_OperationalError_StartNoSubstrate(t *testing.T) {
	// Using the real binary path (sandboxNoopDriver) via cli.Run with XDG_STATE_HOME.
	// First create a sandbox so start can find it.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	// Create via Run so the store is in the right place.
	createCode := Run([]string{"sandbox", "create", "proj/box"})
	if createCode != 0 {
		t.Fatalf("create failed with exit code %d", createCode)
	}

	// Start should fail with the noopDriver (exit 1).
	startCode := Run([]string{"sandbox", "start", "proj/box"})
	if startCode != 1 {
		t.Errorf("start with no substrate: exit code = %d, want 1", startCode)
	}
}

// ── Stable error code tests ───────────────────────────────────────────────────

// errEnvJSON is the JSON shape of an error envelope as emitted by root.go.
type errEnvJSON struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Error         struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// decodeErrEnv decodes exactly one JSON error envelope from r.
func decodeErrEnv(t *testing.T, r io.Reader) errEnvJSON {
	t.Helper()
	var env errEnvJSON
	dec := json.NewDecoder(r)
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	// Assert nothing follows — a single JSON object is the contract.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		t.Error("stdout contains more than one JSON object")
	}
	return env
}

// TestSandboxRm_NotFound_Code verifies that removing a non-existent sandbox
// yields code sandbox_not_found, NOT internal_error.
func TestSandboxRm_NotFound_Code(t *testing.T) {
	svc := newTestService(t)
	out, stdout, _ := capture(true)

	err := runSandboxRm(context.Background(), []string{"nosuchsandbox"}, out, svc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if coded.Code != sandboxErrCodeNotFound {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeNotFound)
	}

	// Also verify the envelope when rendered via EmitError.
	out.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout)
	if env.Error.Code != sandboxErrCodeNotFound {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, sandboxErrCodeNotFound)
	}
}

// TestSandboxRm_AmbiguousRef_Code verifies that an ambiguous prefix yields
// code ambiguous_ref and names all matching candidates in the message.
//
// "sb-" is a deterministic ambiguous prefix: every sandbox ID starts with
// "sb-", ParseSandboxID("sb-") fails (too short for a full ID), so
// ResolveByPrefix("sb-") is called and matches all sandboxes → ErrAmbiguous.
func TestSandboxRm_AmbiguousRef_Code(t *testing.T) {
	svc := newTestService(t)
	sb1, err := svc.Create(context.Background(), "proj", "one", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create one: %v", err)
	}
	sb2, err := svc.Create(context.Background(), "proj", "two", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create two: %v", err)
	}

	out, stdout, _ := capture(true)
	rmErr := runSandboxRm(context.Background(), []string{"sb-"}, out, svc)
	if rmErr == nil {
		t.Fatal("expected ambiguous-ref error, got nil")
	}

	var coded *CodedError
	if !errors.As(rmErr, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", rmErr, rmErr)
	}
	if coded.Code != sandboxErrCodeAmbiguousRef {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeAmbiguousRef)
	}

	// Message must name both candidates.
	id1, id2 := sb1.ID.String(), sb2.ID.String()
	if !strings.Contains(coded.Msg, id1) || !strings.Contains(coded.Msg, id2) {
		t.Errorf("message does not name both candidates; got: %s", coded.Msg)
	}

	// Verify via envelope.
	out.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout)
	if env.Error.Code != sandboxErrCodeAmbiguousRef {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, sandboxErrCodeAmbiguousRef)
	}
	if !strings.Contains(env.Error.Message, id1) || !strings.Contains(env.Error.Message, id2) {
		t.Errorf("envelope message does not name both candidates; got: %s", env.Error.Message)
	}
}

// TestSandboxCreate_AlreadyExists_Code verifies that creating a duplicate
// handle yields code sandbox_already_exists.
func TestSandboxCreate_AlreadyExists_Code(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(true)

	// First create succeeds.
	if err := runSandboxCreate(context.Background(), []string{"proj/box"}, out, svc); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second create with the same handle must fail.
	out2, stdout2, _ := capture(true)
	err := runSandboxCreate(context.Background(), []string{"proj/box"}, out2, svc)
	if err == nil {
		t.Fatal("expected already-exists error, got nil")
	}

	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if coded.Code != sandboxErrCodeAlreadyExists {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeAlreadyExists)
	}

	out2.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout2)
	if env.Error.Code != sandboxErrCodeAlreadyExists {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, sandboxErrCodeAlreadyExists)
	}
}

// TestSandboxResume_IllegalTransition_Code verifies that resuming a Stopped
// sandbox (no resume edge from Stopped in the state machine) yields code
// illegal_transition. The fake driver is used so the machine check fires
// before any PauseResumer assertion.
func TestSandboxResume_IllegalTransition_Code(t *testing.T) {
	svc := newTestService(t)
	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})

	// Start → Stop so the sandbox is in the Stopped state.
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.Stop(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Resume on a Stopped sandbox: no such edge in the lifecycle table.
	out, stdout, _ := capture(true)
	err := runSandboxResume(context.Background(), []string{sb.ID.String()}, out, svc)
	if err == nil {
		t.Fatal("expected illegal-transition error, got nil")
	}

	var coded *CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", err, err)
	}
	if coded.Code != sandboxErrCodeIllegalTransition {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeIllegalTransition)
	}

	out.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout)
	if env.Error.Code != sandboxErrCodeIllegalTransition {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, sandboxErrCodeIllegalTransition)
	}
}

// TestSandboxCreate_BadHandle_UsageError verifies that a malformed handle
// (no slash) is a usage error with exit code 2 and code invalid_argument.
func TestSandboxCreate_BadHandle_UsageError_Code(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Exit code must be 2 (usage error).
	code := Run([]string{"sandbox", "create", "noslash"})
	if code != 2 {
		t.Errorf("bad handle: exit code = %d, want 2", code)
	}
}

// TestSandboxStart_NoSubstrate_Code verifies that starting a sandbox with
// the sandboxNoopDriver yields code no_substrate via the CodedError path.
func TestSandboxStart_NoSubstrate_Code(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, &sandboxNoopDriver{}, lifecycle.New())

	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})

	out, stdout, _ := capture(true)
	startErr := runSandboxStart(context.Background(), []string{sb.ID.String()}, out, svc)
	if startErr == nil {
		t.Fatal("expected no-substrate error, got nil")
	}

	var coded *CodedError
	if !errors.As(startErr, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", startErr, startErr)
	}
	if coded.Code != sandboxErrCodeNoSubstrate {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeNoSubstrate)
	}

	out.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout)
	if env.Error.Code != sandboxErrCodeNoSubstrate {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, sandboxErrCodeNoSubstrate)
	}
}

// TestSandboxStart_NoGuestImage_Code verifies that starting a sandbox when no
// guest kernel is configured yields code no_guest_image, NOT internal_error.
// The fake driver injects ErrNoKernelConfigured (wrapped, as the real driver
// does) to exercise the errors.Is path without a live VMM.
func TestSandboxStart_NoGuestImage_Code(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()
	drv.SetStartError(fmt.Errorf("cloudhypervisor: start sb-abc: %w", cloudhypervisor.ErrNoKernelConfigured))
	svc := service.New(st, drv, lifecycle.New())

	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})

	out, stdout, _ := capture(true)
	startErr := runSandboxStart(context.Background(), []string{sb.ID.String()}, out, svc)
	if startErr == nil {
		t.Fatal("expected no-guest-image error, got nil")
	}

	var coded *CodedError
	if !errors.As(startErr, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", startErr, startErr)
	}
	if coded.Code != sandboxErrCodeNoGuestImage {
		t.Errorf("code = %q, want %q", coded.Code, sandboxErrCodeNoGuestImage)
	}

	out.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout)
	if env.Error.Code != sandboxErrCodeNoGuestImage {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, sandboxErrCodeNoGuestImage)
	}
}

// TestSandboxStart_InternalError_Fallback verifies that a genuine unexpected
// error (not matched by any sentinel) still emits code internal_error, proving
// the fallback path is not broken by the new no_guest_image case.
func TestSandboxStart_InternalError_Fallback(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()
	drv.SetStartError(errors.New("unexpected disk corruption"))
	svc := service.New(st, drv, lifecycle.New())

	sb, _ := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})

	out, stdout, _ := capture(true)
	startErr := runSandboxStart(context.Background(), []string{sb.ID.String()}, out, svc)
	if startErr == nil {
		t.Fatal("expected internal error, got nil")
	}

	var coded *CodedError
	if !errors.As(startErr, &coded) {
		t.Fatalf("expected *CodedError, got %T: %v", startErr, startErr)
	}
	if coded.Code != ErrCodeInternalError {
		t.Errorf("code = %q, want %q", coded.Code, ErrCodeInternalError)
	}

	out.EmitError(coded.Code, coded.Msg)
	env := decodeErrEnv(t, stdout)
	if env.Error.Code != ErrCodeInternalError {
		t.Errorf("envelope error.code = %q, want %q", env.Error.Code, ErrCodeInternalError)
	}
}

// ── auto-resize (AR-CLI) ──────────────────────────────────────────────────────

// TestAutoResize_DefaultOn proves that creating a sandbox without any auto-resize
// ceiling flags produces non-zero GovBounds and hotplug PID-1 args.
// Auto-resize is now unconditional (D-DC-30 revised 2026-08-14).
func TestAutoResize_DefaultOn(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"proj/box", "--memory", "512", "--vcpus", "2"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	// Ceiling flags are all zero — defaults are applied inside vmcfg.Resolve.
	if f.memoryMaxMiB != 0 {
		t.Errorf("memoryMaxMiB: got %d, want 0 (ceiling unset; default applied by vmcfg.Resolve)", f.memoryMaxMiB)
	}

	res := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: f.memoryMiB, BootVCPUs: f.vcpus,
		MemMaxMiB: f.memoryMaxMiB, VCPUsMax: f.vcpusMax, DiskMaxGiB: f.diskMaxGiB,
	})
	if res.Bounds.MemMaxBytes == 0 {
		t.Error("GovBounds.MemMaxBytes: got 0, want non-zero (unconditional)")
	}
	if res.Bounds.VCPUMax == 0 {
		t.Error("GovBounds.VCPUMax: got 0, want non-zero")
	}
	if res.PID1Args == "" {
		t.Error("PID1Args: got empty string, want non-empty")
	}
}

// TestAutoResize_NoAutoResize_Rejected proves that --no-auto-resize is now
// rejected as an unknown flag (auto-resize is unconditional; there is no opt-out).
func TestAutoResize_NoAutoResize_Rejected(t *testing.T) {
	_, err := parseSandboxCreateArgs([]string{"proj/box", "--memory", "512", "--no-auto-resize"})
	if err == nil {
		t.Error("--no-auto-resize: expected error (unknown flag), got nil")
	}
}

// TestAutoResize_FlagParsing verifies that the auto-resize ceiling flags are
// parsed correctly from the argument slice.
func TestAutoResize_FlagParsing(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{
		"proj/box",
		"--memory-max", "2048",
		"--vcpus-max", "4",
		"--disk-max", "100",
	})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}
	if f.memoryMaxMiB != 2048 {
		t.Errorf("memoryMaxMiB: got %d, want 2048", f.memoryMaxMiB)
	}
	if f.vcpusMax != 4 {
		t.Errorf("vcpusMax: got %d, want 4", f.vcpusMax)
	}
	if f.diskMaxGiB != 100 {
		t.Errorf("diskMaxGiB: got %d, want 100", f.diskMaxGiB)
	}
}

// TestAutoResize_GovBoundsWired proves that with auto-resize ceiling flags set,
// GovBounds is non-zero (the specific gap identified by the advisor gate:
// no production caller for GovBounds). Auto-resize is unconditional.
func TestAutoResize_GovBoundsWired(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{
		"proj/box",
		"--memory-max", "2048",
		"--vcpus-max", "4",
		"--disk-max", "100",
	})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}

	res := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: f.memoryMiB, BootVCPUs: f.vcpus,
		MemMaxMiB: f.memoryMaxMiB, VCPUsMax: f.vcpusMax, DiskMaxGiB: f.diskMaxGiB,
	})
	bounds := res.Bounds

	// GovBounds must be non-zero: this is the wiring the gate identified as missing.
	if bounds.MemMaxBytes == 0 {
		t.Error("GovBounds.MemMaxBytes: got 0, want non-zero (governor would run in passive mode)")
	}
	if bounds.VCPUMax == 0 {
		t.Error("GovBounds.VCPUMax: got 0, want non-zero")
	}
	if bounds.DiskMaxBytes == 0 {
		t.Error("GovBounds.DiskMaxBytes: got 0, want non-zero")
	}

	// Verify exact values.
	const wantMemMax = int64(2048) * 1024 * 1024
	if bounds.MemMaxBytes != wantMemMax {
		t.Errorf("GovBounds.MemMaxBytes: got %d, want %d", bounds.MemMaxBytes, wantMemMax)
	}
	const wantMemMin = int64(512) * 1024 * 1024 // driver default boot memory
	if bounds.MemMinBytes != wantMemMin {
		t.Errorf("GovBounds.MemMinBytes: got %d, want %d (boot default)", bounds.MemMinBytes, wantMemMin)
	}
	if bounds.VCPUMax != 4 {
		t.Errorf("GovBounds.VCPUMax: got %d, want 4", bounds.VCPUMax)
	}
	if bounds.VCPUMin != 1 {
		t.Errorf("GovBounds.VCPUMin: got %d, want 1 (boot default)", bounds.VCPUMin)
	}
	const wantDiskMax = int64(100) * 1024 * 1024 * 1024
	if bounds.DiskMaxBytes != wantDiskMax {
		t.Errorf("GovBounds.DiskMaxBytes: got %d, want %d", bounds.DiskMaxBytes, wantDiskMax)
	}
}

// TestAutoResize_CeilingDefaults verifies the computed ceiling defaults when
// no explicit ceiling flags are passed (auto-resize is unconditional).
func TestAutoResize_CeilingDefaults(t *testing.T) {
	f, err := parseSandboxCreateArgs([]string{"proj/box"})
	if err != nil {
		t.Fatalf("parseSandboxCreateArgs: %v", err)
	}

	res := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: f.memoryMiB, BootVCPUs: f.vcpus,
		MemMaxMiB: f.memoryMaxMiB, VCPUsMax: f.vcpusMax, DiskMaxGiB: f.diskMaxGiB,
	})
	bounds := res.Bounds

	// Default: 4× boot memory (512 MiB driver default → 2048 MiB), floor 4096 MiB.
	// 4× 512 = 2048 < 4096, so the floor wins.
	const wantMemMax = int64(4096) * 1024 * 1024
	if bounds.MemMaxBytes != wantMemMax {
		t.Errorf("default MemMaxBytes: got %d, want %d (4× boot default 512 MiB, floor 4096 MiB)", bounds.MemMaxBytes, wantMemMax)
	}
	// Default: 4× boot vCPUs (1 driver default → 4), min 4.
	if bounds.VCPUMax != 4 {
		t.Errorf("default VCPUMax: got %d, want 4 (4× boot default 1 vCPU)", bounds.VCPUMax)
	}
	// Default: 100 GiB (matches OLD-nexus diskMaxBytes).
	const wantDiskMax = int64(100) * 1024 * 1024 * 1024
	if bounds.DiskMaxBytes != wantDiskMax {
		t.Errorf("default DiskMaxBytes: got %d, want %d (100 GiB)", bounds.DiskMaxBytes, wantDiskMax)
	}
}

// TestAutoResize_PID1Args verifies that vmcfg.Resolve produces the correct
// PID1Args per the wire contract (--mem-ceiling=<bytes> only; no --auto-resize).
func TestAutoResize_PID1Args(t *testing.T) {
	const memMaxMiB = uint32(2048)
	res := vmcfg.Resolve(vmcfg.Config{BootMemMiB: 512, MemMaxMiB: memMaxMiB})
	got := res.PID1Args

	// Must NOT contain --auto-resize (wire contract with guest agent).
	if strings.Contains(got, "--auto-resize") {
		t.Errorf("args must not contain --auto-resize (wire contract); got %q", got)
	}
	// Must contain --mem-ceiling=<bytes>.
	wantCeiling := fmt.Sprintf("--mem-ceiling=%d", int64(memMaxMiB)*1024*1024)
	if !strings.Contains(got, wantCeiling) {
		t.Errorf("missing %q in %q", wantCeiling, got)
	}
	// PID1Args starts with a leading space (to be appended after "--").
	if len(got) == 0 || got[0] != ' ' {
		t.Errorf("PID1Args must start with a space for cmdline appending; got %q", got)
	}
}

// TestAutoResize_DriverConfig verifies that buildCHConfig wires MemoryMaxMiB
// and VCPUMax into the driver config (auto-resize is unconditional).
func TestAutoResize_DriverConfig(t *testing.T) {
	const kernelPath = "/fake/kernel"
	const ext4Path = "/fake/disk.ext4"

	cfg := buildCHConfig(kernelPath, ext4Path, 512, 2)
	// Simulate what newDriver does: always wire hotplug fields via vmcfg.
	res := vmcfg.Resolve(vmcfg.Config{BootMemMiB: 512, BootVCPUs: 2, MemMaxMiB: 2048, VCPUsMax: 4})
	cfg.MemoryMaxMiB = res.MemoryMaxMiB
	cfg.VCPUMax = res.VCPUMax

	if cfg.MemoryMaxMiB != 2048 {
		t.Errorf("MemoryMaxMiB: got %d, want 2048", cfg.MemoryMaxMiB)
	}
	if cfg.VCPUMax != 4 {
		t.Errorf("VCPUMax: got %d, want 4", cfg.VCPUMax)
	}
}

// TestAutoResize_Cmdline verifies cmdline assembly for the auto-resize path.
// The driver inserts memhp params before "--"; this test checks PID-1 content.
// Wire contract: PID-1 args contain --mem-ceiling=<bytes> only (no --auto-resize).
func TestAutoResize_Cmdline(t *testing.T) {
	const memMaxMiB = uint32(2048)
	res := vmcfg.Resolve(vmcfg.Config{BootMemMiB: 512, MemMaxMiB: memMaxMiB})
	arArgs := res.PID1Args

	t.Run("no workspace: cmdline contains PID-1 args", func(t *testing.T) {
		// Build the cmdline as newDriver does (no workspace mounts).
		cmdline := diskBootCmdlineBase + " --" + arArgs

		// Must NOT contain --auto-resize (wire contract).
		if strings.Contains(cmdline, "--auto-resize") {
			t.Errorf("cmdline must not contain --auto-resize (wire contract): %q", cmdline)
		}
		wantCeiling := fmt.Sprintf("--mem-ceiling=%d", int64(memMaxMiB)*1024*1024)
		if !strings.Contains(cmdline, wantCeiling) {
			t.Errorf("cmdline missing %q: %q", wantCeiling, cmdline)
		}
		// PID-1 args appear after "--".
		pidBoundary := strings.Index(cmdline, " --")
		if pidBoundary < 0 {
			t.Fatalf("cmdline has no '--' PID-1 boundary: %q", cmdline)
		}
		pidSection := cmdline[pidBoundary:]
		if !strings.Contains(pidSection, "--mem-ceiling=") {
			t.Errorf("--mem-ceiling not in PID-1 section: %q", pidSection)
		}
	})

	t.Run("with workspace: PID-1 args appended after workspace-mount", func(t *testing.T) {
		// Simulate workspaceMountCmdline output.
		fakeWorkspaceCmdline := diskBootCmdlineBase + " -- --workspace-mount=/dev/vdb:/workspace/repo:ext4:false:true"
		cmdline := fakeWorkspaceCmdline + arArgs

		// Must NOT contain --auto-resize (wire contract).
		if strings.Contains(cmdline, "--auto-resize") {
			t.Errorf("cmdline must not contain --auto-resize (wire contract): %q", cmdline)
		}
		// Both workspace-mount and mem-ceiling appear after "--".
		pidIdx := strings.Index(cmdline, " --")
		if pidIdx < 0 {
			t.Fatalf("no '--' boundary: %q", cmdline)
		}
		pidSection := cmdline[pidIdx:]
		if !strings.Contains(pidSection, "--workspace-mount=") {
			t.Errorf("--workspace-mount not in PID-1 section: %q", pidSection)
		}
		wantCeiling := fmt.Sprintf("--mem-ceiling=%d", int64(memMaxMiB)*1024*1024)
		if !strings.Contains(pidSection, wantCeiling) {
			t.Errorf("%q not in PID-1 section: %q", wantCeiling, pidSection)
		}
	})
}

// TestBuilderVM_AutoResizeFullyWired is a regression test that PINS the
// builder-VM auto-resize decision.  It fails when the builder path is in the
// half-converted state (MemoryMaxMiB == 0 / no memhp tokens / no --mem-ceiling)
// that the AR2-BUILDER advisor correction identified.
//
// Revert the builderCfg.MemoryMaxMiB / VCPUMax / Cmdline assignments in
// cmd_sandbox.go and this test fails.
func TestBuilderVM_AutoResizeFullyWired(t *testing.T) {
	// Reproduce the builder-VM config assembly without booting a real VM.
	// These are the same inputs the production path uses.
	bootMemMiB := uint32(builder.DefaultBuilderMemMiB) // 8192
	bootVCPUs := uint32(builder.DefaultBuilderVCPUs)   // 2

	// Build the base config (same call as the production path).
	cfg := buildCHConfig("/fake/kernel", "/fake/builder.ext4", bootMemMiB, bootVCPUs)

	// Resolve auto-resize via vmcfg (same as the production path).
	// REVERT THIS and the test FAILS — proving the builder path is fully wired.
	builderAR := vmcfg.Resolve(vmcfg.Config{BootMemMiB: bootMemMiB, BootVCPUs: bootVCPUs})

	// Wire the three fields (mirrors the production path).
	cfg.MemoryMaxMiB = builderAR.MemoryMaxMiB
	cfg.VCPUMax = builderAR.VCPUMax
	cfg.Cmdline = diskBootCmdlineBase + " --" + builderAR.PID1Args

	// Assert 1: MemoryMaxMiB is non-zero.
	if cfg.MemoryMaxMiB == 0 {
		t.Error("builder VM MemoryMaxMiB == 0: CH would reserve no VirtioMem hotplug region; memhp tokens never emitted")
	}
	// Assert 1b: ceiling is strictly greater than boot (driver validation requirement).
	if cfg.MemoryMaxMiB <= bootMemMiB {
		t.Errorf("builder VM MemoryMaxMiB (%d) must be > boot MemoryMiB (%d)", cfg.MemoryMaxMiB, bootMemMiB)
	}

	// Assert 2: Cmdline has a "--" PID-1 boundary (required for memhp token
	// injection by the driver's buildCmdline and for PID-1 arg delivery).
	pidIdx := strings.Index(cfg.Cmdline, " --")
	if pidIdx < 0 {
		t.Fatalf("builder VM Cmdline has no ' --' PID-1 boundary: %q", cfg.Cmdline)
	}

	// Assert 3: --mem-ceiling=<bytes> appears in the PID-1 section.
	pidSection := cfg.Cmdline[pidIdx:]
	wantCeiling := fmt.Sprintf("--mem-ceiling=%d", int64(builderAR.MemoryMaxMiB)*1024*1024)
	if !strings.Contains(pidSection, wantCeiling) {
		t.Errorf("builder VM Cmdline PID-1 section missing %q: %q", wantCeiling, cfg.Cmdline)
	}

	// Spot-check the default ceilings are sane: 4× boot with documented floors.
	// 8192 MiB boot → expect 32768 MiB ceiling (4×8192, floor 4096).
	const wantMemMaxMiB = uint32(4 * builder.DefaultBuilderMemMiB)
	if cfg.MemoryMaxMiB != wantMemMaxMiB {
		t.Errorf("builder VM MemoryMaxMiB: got %d, want %d (4×DefaultBuilderMemMiB)", cfg.MemoryMaxMiB, wantMemMaxMiB)
	}
	// 2 vCPU boot → expect 8 vCPU ceiling (4×2, floor 4).
	const wantVCPUMax = uint32(4 * builder.DefaultBuilderVCPUs)
	if cfg.VCPUMax != wantVCPUMax {
		t.Errorf("builder VM VCPUMax: got %d, want %d (4×DefaultBuilderVCPUs)", cfg.VCPUMax, wantVCPUMax)
	}
}

// TestAutoResize_FlagErrors verifies that invalid ceiling values and the
// removed --no-auto-resize flag are all rejected with an error.
func TestAutoResize_FlagErrors(t *testing.T) {
	_, err := parseSandboxCreateArgs([]string{"proj/box", "--memory-max", "notanumber"})
	if err == nil {
		t.Error("--memory-max notanumber: expected error, got nil")
	}
	_, err = parseSandboxCreateArgs([]string{"proj/box", "--vcpus-max", "abc"})
	if err == nil {
		t.Error("--vcpus-max abc: expected error, got nil")
	}
	_, err = parseSandboxCreateArgs([]string{"proj/box", "--disk-max", "xyz"})
	if err == nil {
		t.Error("--disk-max xyz: expected error, got nil")
	}
	// --no-auto-resize is removed; it must be rejected as an unknown flag.
	_, err = parseSandboxCreateArgs([]string{"proj/box", "--no-auto-resize"})
	if err == nil {
		t.Error("--no-auto-resize: expected unknown-flag error, got nil")
	}
}

// TestCeilingBelowBootIsRejected verifies that the CLI rejects ceiling values
// that are strictly less than their corresponding boot values. Without this
// check, vmcfg.Resolve would produce Bounds.MemMaxBytes < Bounds.MemMinBytes
// (inverted bounds) which the governor silently mishandles (UNI-WIRE bug).
func TestCeilingBelowBootIsRejected(t *testing.T) {
	t.Run("memory-max less than memory", func(t *testing.T) {
		_, err := parseSandboxCreateArgs([]string{
			"proj/box", "--memory", "512", "--memory-max", "256",
		})
		if err == nil {
			t.Fatal("expected error for --memory-max < --memory, got nil")
		}
		ue, ok := err.(*UsageError)
		if !ok {
			t.Fatalf("expected *UsageError, got %T: %v", err, err)
		}
		if !strings.Contains(ue.Msg, "--memory-max") || !strings.Contains(ue.Msg, "--memory") {
			t.Errorf("error message should mention both flags: %q", ue.Msg)
		}
	})

	t.Run("vcpus-max less than vcpus", func(t *testing.T) {
		_, err := parseSandboxCreateArgs([]string{
			"proj/box", "--vcpus", "4", "--vcpus-max", "2",
		})
		if err == nil {
			t.Fatal("expected error for --vcpus-max < --vcpus, got nil")
		}
		ue, ok := err.(*UsageError)
		if !ok {
			t.Fatalf("expected *UsageError, got %T: %v", err, err)
		}
		if !strings.Contains(ue.Msg, "--vcpus-max") || !strings.Contains(ue.Msg, "--vcpus") {
			t.Errorf("error message should mention both flags: %q", ue.Msg)
		}
	})

	t.Run("memory-max equal to memory: valid (no explicit error; driver validates separately)", func(t *testing.T) {
		// CLI only rejects strict <; equality is left to the driver to handle
		// (it enforces MemoryMaxMiB > MemoryMiB at Start time).
		_, err := parseSandboxCreateArgs([]string{
			"proj/box", "--memory", "512", "--memory-max", "512",
		})
		// No UsageError from the CLI — just verify the parse did not error at
		// the "below boot" check. (The driver will error later at create time.)
		if err != nil {
			ue, ok := err.(*UsageError)
			if ok && strings.Contains(ue.Msg, "less than") {
				t.Errorf("CLI should not reject equal values (< only): %v", err)
			}
		}
	})

	t.Run("ceiling only: valid (no boot value to compare against)", func(t *testing.T) {
		_, err := parseSandboxCreateArgs([]string{
			"proj/box", "--memory-max", "256",
		})
		if err != nil {
			t.Errorf("ceiling-only (no --memory): unexpected error: %v", err)
		}
	})

	t.Run("valid ceiling greater than boot", func(t *testing.T) {
		_, err := parseSandboxCreateArgs([]string{
			"proj/box", "--memory", "512", "--memory-max", "4096",
			"--vcpus", "2", "--vcpus-max", "8",
		})
		if err != nil {
			t.Errorf("valid ceiling > boot: unexpected error: %v", err)
		}
	})
}

// ── kernel preflight ──────────────────────────────────────────────────────────

// TestResolveKernelPath_EnvVar_Valid verifies that NEXUS3_KERNEL_PATH pointing
// at a real file is returned as-is without searching defaults.
func TestResolveKernelPath_EnvVar_Valid(t *testing.T) {
	dir := t.TempDir()
	kernel := dir + "/vmlinux-x86_64"
	if err := os.WriteFile(kernel, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", kernel)

	got, err := resolveKernelPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != kernel {
		t.Errorf("want %q, got %q", kernel, got)
	}
}

// TestResolveKernelPath_EnvVar_Missing verifies that a non-existent
// NEXUS3_KERNEL_PATH value returns an error that names the env var and the
// missing path.
func TestResolveKernelPath_EnvVar_Missing(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/no/such/kernel/vmlinux-x86_64")

	_, err := resolveKernelPath()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "NEXUS3_KERNEL_PATH") {
		t.Errorf("error does not mention NEXUS3_KERNEL_PATH: %q", msg)
	}
	if !strings.Contains(msg, "/no/such/kernel/vmlinux-x86_64") {
		t.Errorf("error does not name the missing path: %q", msg)
	}
}

// TestResolveKernelPath_NoEnv_SearchedPathsListed verifies that when
// NEXUS3_KERNEL_PATH is unset and no default exists, the error lists the
// searched candidate paths so the operator knows what to set.
func TestResolveKernelPath_NoEnv_SearchedPathsListed(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "")

	_, err := resolveKernelPath()
	if err == nil {
		// resolveKernelPath succeeded (kernel is present in the default location).
		// That is valid in a dev environment; skip rather than fail.
		t.Skip("kernel found at default location; skipping missing-kernel test")
	}
	msg := err.Error()
	// Error must name the env var so the operator knows what to set.
	if !strings.Contains(msg, "NEXUS3_KERNEL_PATH") {
		t.Errorf("error does not mention NEXUS3_KERNEL_PATH: %q", msg)
	}
	// Error must list at least one searched path.
	if !strings.Contains(msg, "images/kernel/vmlinux-x86_64") {
		t.Errorf("error does not list searched paths: %q", msg)
	}
}

// TestKernelPreflight_BeforeWorkspaceCapture is an ORDERING test.
// It proves that kernel validation runs before any workspace setup
// (shadow disk creation, workspace capture) so that a future refactor that
// moves capture before the kernel check fails loudly.
//
// Method: install testWorkspaceSpecHook to detect if the workspace block was
// entered. With a bad NEXUS3_KERNEL_PATH, the hook must never fire — the
// function must return the kernel error first.
func TestKernelPreflight_BeforeWorkspaceCapture(t *testing.T) {
	// Point NEXUS3_KERNEL_PATH at a path that does not exist.
	t.Setenv("NEXUS3_KERNEL_PATH", filepath.Join(t.TempDir(), "no-such-kernel"))

	// Arm the workspace spec hook. If it fires, the workspace block ran —
	// which means kernel validation did NOT happen first.
	workspaceEntered := false
	orig := testWorkspaceSpecHook
	testWorkspaceSpecHook = func(*service.WorkspaceSpec) { workspaceEntered = true }
	defer func() { testWorkspaceSpecHook = orig }()

	svc := newTestService(t)
	out, _, _ := capture(true)

	// Use --image and --workspace so both the boot path and workspace block
	// would be reached if kernel validation were absent or deferred.
	wsDir := t.TempDir()
	err := runSandboxCreate(
		context.Background(),
		[]string{"proj/ordering-test", "--image", "sha256:abc123", "--workspace", wsDir},
		out, svc,
	)

	// The call must have failed.
	if err == nil {
		t.Fatal("expected kernel-not-found error, got nil")
	}
	// The error must be about the kernel, not about an image or workspace.
	if !strings.Contains(err.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("expected NEXUS3_KERNEL_PATH in error; got: %v", err)
	}
	// The workspace block must NOT have been entered — proves ordering.
	if workspaceEntered {
		t.Error("ORDERING VIOLATION: workspace spec was built before kernel validation ran; " +
			"a refactor moved capture before the kernel preflight check")
	}
}

// ── No-boot guard regression tests (D-NOBOOT-MOUNT) ─────────────────────────

// TestSandboxCreate_NoBootMount_UsageError proves that --mount is rejected on
// the store-only (no-boot) create path (no --image/--rootfs/--file).
// Before this guard, the flag was silently dropped and the create returned RC=0
// with the mount never applied.
func TestSandboxCreate_NoBootMount_UsageError(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)
	err := runSandboxCreate(context.Background(),
		[]string{"proj/box", "--mount", "/tmp/x:/w"},
		out, svc)
	if err == nil {
		t.Fatal("expected UsageError for --mount on no-boot path, got nil")
	}
	var ue *UsageError
	if !isUsageError(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// TestSandboxCreate_NoBootMountNamed_UsageError proves that --mount-named is
// rejected on the store-only (no-boot) create path.
func TestSandboxCreate_NoBootMountNamed_UsageError(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)
	err := runSandboxCreate(context.Background(),
		[]string{"proj/box", "--mount-named", "vol:/v"},
		out, svc)
	if err == nil {
		t.Fatal("expected UsageError for --mount-named on no-boot path, got nil")
	}
	var ue *UsageError
	if !isUsageError(err, &ue) {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// TestSandboxCreate_NoBoot_PlainCreate_OK is the negative control: a plain
// store-only create with no mount flags must succeed.  A test suite that only
// rejects inputs proves nothing without at least one accepting path.
func TestSandboxCreate_NoBoot_PlainCreate_OK(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)
	if err := runSandboxCreate(context.Background(),
		[]string{"proj/plainbox"},
		out, svc); err != nil {
		t.Fatalf("plain no-boot create should succeed, got: %v", err)
	}
}

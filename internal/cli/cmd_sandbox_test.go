package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// ── stub service ──────────────────────────────────────────────────────────────

// stubService is a recording test double for SandboxService. Each method
// captures its call arguments and returns the pre-configured result.
type stubService struct {
	// Create
	createCalledWith struct {
		project, name string
		opts          service.CreateOptions
	}
	createResult domain.Sandbox
	createErr    error

	// CreateAndBoot
	createAndBootCalledWith struct {
		project, name string
		opts          service.CreateAndBootOptions
	}
	createAndBootResult domain.Sandbox
	createAndBootErr    error

	// List
	listResult []domain.Sandbox
	listErr    error

	// Start / Stop / Pause / Resume — capture ref
	startRef, stopRef, pauseRef, resumeRef             string
	startResult, stopResult, pauseResult, resumeResult domain.Sandbox
	startErr, stopErr, pauseErr, resumeErr             error

	// Remove
	removeRef string
	removeErr error
}

func (s *stubService) Create(_ context.Context, project, name string, opts service.CreateOptions) (domain.Sandbox, error) {
	s.createCalledWith.project = project
	s.createCalledWith.name = name
	s.createCalledWith.opts = opts
	return s.createResult, s.createErr
}

func (s *stubService) CreateAndBoot(_ context.Context, project, name string, opts service.CreateAndBootOptions) (domain.Sandbox, error) {
	s.createAndBootCalledWith.project = project
	s.createAndBootCalledWith.name = name
	s.createAndBootCalledWith.opts = opts
	return s.createAndBootResult, s.createAndBootErr
}

func (s *stubService) List(_ context.Context) ([]domain.Sandbox, error) {
	return s.listResult, s.listErr
}

func (s *stubService) Start(_ context.Context, ref string) (domain.Sandbox, error) {
	s.startRef = ref
	return s.startResult, s.startErr
}

func (s *stubService) Stop(_ context.Context, ref string) (domain.Sandbox, error) {
	s.stopRef = ref
	return s.stopResult, s.stopErr
}

func (s *stubService) Pause(_ context.Context, ref string) (domain.Sandbox, error) {
	s.pauseRef = ref
	return s.pauseResult, s.pauseErr
}

func (s *stubService) Resume(_ context.Context, ref string) (domain.Sandbox, error) {
	s.resumeRef = ref
	return s.resumeResult, s.resumeErr
}

func (s *stubService) Remove(_ context.Context, ref string) error {
	s.removeRef = ref
	return s.removeErr
}

// ── test helpers ──────────────────────────────────────────────────────────────

// connectPair creates an in-memory server+client session pair backed by stub.
// The caller must call close() when done.
func connectPair(t *testing.T, stub *stubService) (*gosdk.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()

	clientTransport, serverTransport := gosdk.NewInMemoryTransports()

	srv := NewServer(stub)
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	client := gosdk.NewClient(&gosdk.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	return cs, func() {
		cs.Close()
		ss.Wait()
	}
}

// callTool is a shorthand for invoking a tool and returning the result text.
func callTool(t *testing.T, cs *gosdk.ClientSession, name string, args map[string]any) *gosdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &gosdk.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", name, err)
	}
	return res
}

func resultText(t *testing.T, res *gosdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	tc, ok := res.Content[0].(*gosdk.TextContent)
	if !ok {
		t.Fatalf("expected *TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

// resultData unwraps the Response envelope and returns the data field as JSON bytes.
// It fails the test if the envelope cannot be parsed or ok=false.
func resultData(t *testing.T, res *gosdk.CallToolResult) []byte {
	t.Helper()
	text := resultText(t, res)
	var env Response
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("envelope unmarshal: %v (text=%q)", err, text)
	}
	if !env.OK {
		t.Fatalf("response not ok: %+v", env.Error)
	}
	b, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("re-marshal data: %v", err)
	}
	return b
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestSandboxList_empty(t *testing.T) {
	stub := &stubService{listResult: []domain.Sandbox{}}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_list", nil)
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	var got []any
	if err := json.Unmarshal(resultData(t, res), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %d items", len(got))
	}
}

func TestSandboxList_nonEmpty(t *testing.T) {
	id := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:      id,
		Project: "proj",
		Name:    "test",
		State:   domain.Created,
	}
	stub := &stubService{listResult: []domain.Sandbox{sb}}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_list", nil)
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	var got []sandboxJSON
	if err := json.Unmarshal(resultData(t, res), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got))
	}
	if got[0].Project != "proj" || got[0].Name != "test" {
		t.Fatalf("unexpected sandbox: %+v", got[0])
	}
}

func TestSandboxList_serviceError(t *testing.T) {
	stub := &stubService{listErr: errors.New("store exploded")}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_list", nil)
	if !res.IsError {
		t.Fatal("expected error result, got success")
	}
}

func TestSandboxCreate_invokesService(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createResult: domain.Sandbox{
			ID:      id,
			Project: "myproj",
			Name:    "mysb",
			State:   domain.Created,
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":       "myproj",
		"name":          "mysb",
		"remove_on_exit": false,
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	// Verify the stub was called with the right args.
	if stub.createCalledWith.project != "myproj" {
		t.Errorf("Create project: want %q, got %q", "myproj", stub.createCalledWith.project)
	}
	if stub.createCalledWith.name != "mysb" {
		t.Errorf("Create name: want %q, got %q", "mysb", stub.createCalledWith.name)
	}

	// Verify the response JSON.
	var got sandboxJSON
	if err := json.Unmarshal(resultData(t, res), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Project != "myproj" || got.Name != "mysb" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestSandboxCreate_missingProject(t *testing.T) {
	stub := &stubService{}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project": "",
		"name":    "mysb",
	})
	if !res.IsError {
		t.Fatal("expected error for missing project, got success")
	}
}

func TestSandboxStart_invokesService(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		startResult: domain.Sandbox{ID: id, Project: "p", Name: "n", State: domain.Running},
	}
	cs, close := connectPair(t, stub)
	defer close()

	callTool(t, cs, "sandbox_start", map[string]any{"ref": "p/n"})
	if stub.startRef != "p/n" {
		t.Errorf("Start ref: want %q, got %q", "p/n", stub.startRef)
	}
}

func TestSandboxStop_invokesService(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		stopResult: domain.Sandbox{ID: id, Project: "p", Name: "n", State: domain.Stopped},
	}
	cs, close := connectPair(t, stub)
	defer close()

	callTool(t, cs, "sandbox_stop", map[string]any{"ref": "p/n"})
	if stub.stopRef != "p/n" {
		t.Errorf("Stop ref: want %q, got %q", "p/n", stub.stopRef)
	}
}

func TestSandboxPause_invokesService(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		pauseResult: domain.Sandbox{ID: id, Project: "p", Name: "n", State: domain.Paused},
	}
	cs, close := connectPair(t, stub)
	defer close()

	callTool(t, cs, "sandbox_pause", map[string]any{"ref": "p/n"})
	if stub.pauseRef != "p/n" {
		t.Errorf("Pause ref: want %q, got %q", "p/n", stub.pauseRef)
	}
}

func TestSandboxResume_invokesService(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		resumeResult: domain.Sandbox{ID: id, Project: "p", Name: "n", State: domain.Running},
	}
	cs, close := connectPair(t, stub)
	defer close()

	callTool(t, cs, "sandbox_resume", map[string]any{"ref": "p/n"})
	if stub.resumeRef != "p/n" {
		t.Errorf("Resume ref: want %q, got %q", "p/n", stub.resumeRef)
	}
}

func TestSandboxRemove_invokesService(t *testing.T) {
	stub := &stubService{}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_remove", map[string]any{"ref": "p/n"})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}
	if stub.removeRef != "p/n" {
		t.Errorf("Remove ref: want %q, got %q", "p/n", stub.removeRef)
	}
}

func TestSandboxRemove_missingRef(t *testing.T) {
	stub := &stubService{}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_remove", map[string]any{"ref": ""})
	if !res.IsError {
		t.Fatal("expected error for missing ref, got success")
	}
}

func TestSandboxCreate_WithImage_CallsCreateAndBoot(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createAndBootResult: domain.Sandbox{
			ID:      id,
			Project: "myproj",
			Name:    "mysb",
			State:   domain.Running,
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":      "myproj",
		"name":         "mysb",
		"rootfs_path":  "/path/to/rootfs.ext4",
		"remove_on_exit": true,
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	// CreateAndBoot must have been called with the correct args.
	cab := stub.createAndBootCalledWith
	if cab.project != "myproj" {
		t.Errorf("CreateAndBoot project: want %q, got %q", "myproj", cab.project)
	}
	if cab.name != "mysb" {
		t.Errorf("CreateAndBoot name: want %q, got %q", "mysb", cab.name)
	}
	if cab.opts.Image.RootfsPath != "/path/to/rootfs.ext4" {
		t.Errorf("CreateAndBoot RootfsPath: want %q, got %q", "/path/to/rootfs.ext4", cab.opts.Image.RootfsPath)
	}
	if !cab.opts.RemoveOnExit {
		t.Error("CreateAndBoot RemoveOnExit: want true, got false")
	}

	// Create must NOT have been called (we took the boot path, not the record-only path).
	if stub.createCalledWith.project != "" {
		t.Errorf("Create was called unexpectedly: project=%q", stub.createCalledWith.project)
	}

	// Response must reflect the running state returned by the stub.
	var got sandboxJSON
	if err := json.Unmarshal(resultData(t, res), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.State != domain.Running.String() {
		t.Errorf("state: want %q, got %q", domain.Running.String(), got.State)
	}
	if got.Project != "myproj" || got.Name != "mysb" {
		t.Fatalf("unexpected sandbox in response: %+v", got)
	}
}

func TestSandboxCreate_WithDigest_CallsCreateAndBoot(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createAndBootResult: domain.Sandbox{
			ID:      id,
			Project: "proj",
			Name:    "sb",
			State:   domain.Running,
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":       "proj",
		"name":          "sb",
		"digest":        "sha256:abc123",
		"remove_on_exit": false,
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}
	if stub.createAndBootCalledWith.opts.Image.Digest != "sha256:abc123" {
		t.Errorf("CreateAndBoot Digest: want %q, got %q",
			"sha256:abc123", stub.createAndBootCalledWith.opts.Image.Digest)
	}
	if stub.createCalledWith.project != "" {
		t.Error("Create was called unexpectedly")
	}
}

func TestToolsRegistered(t *testing.T) {
	stub := &stubService{}
	cs, close := connectPair(t, stub)
	defer close()

	want := map[string]bool{
		"sandbox_create": false,
		"sandbox_list":   false,
		"sandbox_start":  false,
		"sandbox_stop":   false,
		"sandbox_pause":  false,
		"sandbox_resume": false,
		"sandbox_remove": false,
	}
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		want[tool.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not registered", name)
		}
	}
}

// ── MEMAC2: memory_mib / vcpus thread through to CreateAndBootOptions ─────────

// TestSandboxCreate_WithMemoryVCPUs_CallsCreateAndBoot verifies that
// memory_mib and vcpus in sandbox_create args are threaded through to
// CreateAndBootOptions.MemoryMiB / .VCPUs when the boot path is taken.
func TestSandboxCreate_WithMemoryVCPUs_CallsCreateAndBoot(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createAndBootResult: domain.Sandbox{
			ID:      id,
			Project: "myproj",
			Name:    "mysb",
			State:   domain.Running,
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":        "myproj",
		"name":           "mysb",
		"rootfs_path":    "/path/to/rootfs.ext4",
		"remove_on_exit": false,
		"memory_mib":     2048,
		"vcpus":          2,
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	cab := stub.createAndBootCalledWith
	if cab.opts.MemoryMiB != 2048 {
		t.Errorf("CreateAndBoot MemoryMiB: want 2048, got %d", cab.opts.MemoryMiB)
	}
	if cab.opts.VCPUs != 2 {
		t.Errorf("CreateAndBoot VCPUs: want 2, got %d", cab.opts.VCPUs)
	}
	if cab.opts.Image.RootfsPath != "/path/to/rootfs.ext4" {
		t.Errorf("CreateAndBoot RootfsPath: want %q, got %q", "/path/to/rootfs.ext4", cab.opts.Image.RootfsPath)
	}

	// Create must NOT have been called (boot path was taken).
	if stub.createCalledWith.project != "" {
		t.Errorf("Create was called unexpectedly: project=%q", stub.createCalledWith.project)
	}
}

// TestSandboxCreate_WithMemoryVCPUs_DefaultsUnset verifies that omitting
// memory_mib and vcpus leaves them zero in CreateAndBootOptions (driver
// applies its own 512 MiB / 1 vCPU defaults — no regression).
func TestSandboxCreate_WithMemoryVCPUs_DefaultsUnset(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createAndBootResult: domain.Sandbox{
			ID:      id,
			Project: "proj",
			Name:    "sb",
			State:   domain.Running,
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":        "proj",
		"name":           "sb",
		"rootfs_path":    "/rootfs.ext4",
		"remove_on_exit": false,
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	cab := stub.createAndBootCalledWith
	if cab.opts.MemoryMiB != 0 {
		t.Errorf("MemoryMiB default: want 0, got %d", cab.opts.MemoryMiB)
	}
	if cab.opts.VCPUs != 0 {
		t.Errorf("VCPUs default: want 0, got %d", cab.opts.VCPUs)
	}
}

// ── S-SURFACE: motive parameter ───────────────────────────────────────────────

// TestSandboxCreate_WithMotive_SetsMotiveID verifies that passing a motive arg
// to sandbox_create threads the value through to CreateAndBootOptions.Labels["motive"].
// Uses the boot path (rootfs_path set) so CreateAndBoot is invoked.
func TestSandboxCreate_WithMotive_SetsMotiveID(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createAndBootResult: domain.Sandbox{
			ID:      id,
			Project: "myproj",
			Name:    "mysb",
			State:   domain.Running,
			Labels:  map[string]string{"motive": "m-abc-123"},
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":        "myproj",
		"name":           "mysb",
		"remove_on_exit": false,
		"rootfs_path":    "/fake/rootfs.ext4",
		"motive":         "m-abc-123",
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	cab := stub.createAndBootCalledWith
	if cab.opts.Labels["motive"] != "m-abc-123" {
		t.Errorf("Labels[motive]: want %q, got %q", "m-abc-123", cab.opts.Labels["motive"])
	}
	if cab.opts.Image.RootfsPath != "/fake/rootfs.ext4" {
		t.Errorf("RootfsPath: want %q, got %q", "/fake/rootfs.ext4", cab.opts.Image.RootfsPath)
	}
	// Create must NOT have been called (boot path was taken).
	if stub.createCalledWith.project != "" {
		t.Errorf("Create was called unexpectedly: project=%q", stub.createCalledWith.project)
	}
}

// TestSandboxCreate_Motive_DefaultsEmpty verifies that omitting the motive arg
// leaves Labels nil in CreateAndBootOptions (unassociated, no regression).
func TestSandboxCreate_Motive_DefaultsEmpty(t *testing.T) {
	id := domain.NewSandboxID()
	stub := &stubService{
		createAndBootResult: domain.Sandbox{
			ID:      id,
			Project: "myproj",
			Name:    "mysb",
			State:   domain.Running,
		},
	}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_create", map[string]any{
		"project":        "myproj",
		"name":           "mysb",
		"remove_on_exit": false,
		"rootfs_path":    "/fake/rootfs.ext4",
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	if stub.createAndBootCalledWith.opts.Labels["motive"] != "" {
		t.Errorf("Labels[motive] default: want empty, got %q", stub.createAndBootCalledWith.opts.Labels["motive"])
	}
}

// TestSandboxList_truncation verifies that sandbox_list applies the byte cap
// and returns non-nil truncated metadata when the list is large (I1-AC2).
func TestSandboxList_truncation(t *testing.T) {
	// Build a list large enough to exceed listMaxResponseBytes (64 KiB).
	// Each sandboxJSON is ~120 bytes; 800 items ≈ 96 KiB > 64 KiB cap.
	const n = 800
	sbs := make([]domain.Sandbox, n)
	for i := range sbs {
		sbs[i] = domain.Sandbox{
			ID:      domain.NewSandboxID(),
			Project: "proj",
			Name:    fmt.Sprintf("sandbox-%04d", i),
			State:   domain.Created,
		}
	}
	stub := &stubService{listResult: sbs}
	cs, close := connectPair(t, stub)
	defer close()

	res := callTool(t, cs, "sandbox_list", nil)
	if res.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, res))
	}

	text := resultText(t, res)
	var env Response
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("envelope unmarshal: %v", err)
	}
	if !env.OK {
		t.Fatalf("response not ok: %+v", env.Error)
	}
	if env.Truncated == nil {
		t.Fatal("expected truncated metadata for large list, got nil")
	}
	if env.Truncated.BytesOmitted <= 0 {
		t.Errorf("bytes_omitted: want > 0, got %d", env.Truncated.BytesOmitted)
	}
	if env.Truncated.TotalBytes <= int64(listMaxResponseBytes) {
		t.Errorf("total_bytes %d should exceed cap %d", env.Truncated.TotalBytes, listMaxResponseBytes)
	}

	// The data should be a valid, non-empty, proper subset of the full list.
	var items []sandboxJSON
	db, _ := json.Marshal(env.Data)
	if err := json.Unmarshal(db, &items); err != nil {
		t.Fatalf("data unmarshal: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("truncated list must contain at least one item")
	}
	if len(items) >= n {
		t.Errorf("truncated list has %d items, expected fewer than %d", len(items), n)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newSnapSvc(t *testing.T) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	aStore, err := artifact.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("artifact.NewStore: %v", err)
	}
	return service.New(st, fake.New(), lifecycle.New()).WithArtifacts(aStore)
}

// startedSandbox creates and starts a sandbox, returning its ID string.
func startedSandbox(t *testing.T, svc *service.Service, handle string) string {
	t.Helper()
	project, name, err := func() (string, string, error) {
		for i, c := range handle {
			if c == '/' {
				return handle[:i], handle[i+1:], nil
			}
		}
		return "", "", nil
	}()
	if project == "" {
		t.Fatalf("bad handle %q", handle)
	}
	sb, err := svc.Create(context.Background(), project, name, service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create %q: %v", handle, err)
	}
	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start %q: %v", handle, err)
	}
	return sb.ID.String()
}

// ── snapshot create ───────────────────────────────────────────────────────────

func TestSnapshotCreate_JSON_Schema(t *testing.T) {
	svc := newSnapSvc(t)
	ref := startedSandbox(t, svc, "proj/snap-json")

	out, stdout, _ := capture(true)
	if err := runSnapshotCreate(context.Background(), []string{ref}, out, svc); err != nil {
		t.Fatalf("runSnapshotCreate: %v", err)
	}

	var env struct {
		SchemaVersion int                     `json:"schema_version"`
		Kind          string                  `json:"kind"`
		Data          snapshotCreatedDataJSON `json:"data"`
	}
	decodeOne(t, stdout, &env)
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d, want 1", env.SchemaVersion)
	}
	if env.Kind != "snapshot.created" {
		t.Errorf("kind: got %q, want %q", env.Kind, "snapshot.created")
	}
	if env.Data.Snapshot.ID == "" {
		t.Error("snapshot.id is empty")
	}
	if env.Data.Snapshot.Kind != string(artifact.KindRetained) {
		t.Errorf("snapshot.kind: got %q, want %q", env.Data.Snapshot.Kind, artifact.KindRetained)
	}
}

func TestSnapshotCreate_UsageError_MissingRef(t *testing.T) {
	svc := newSnapSvc(t)
	out, _, _ := capture(false)
	err := runSnapshotCreate(context.Background(), []string{}, out, svc)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestSnapshotCreate_UsageError_TooManyArgs(t *testing.T) {
	svc := newSnapSvc(t)
	out, _, _ := capture(false)
	err := runSnapshotCreate(context.Background(), []string{"a", "b"}, out, svc)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

// ── fork ──────────────────────────────────────────────────────────────────────

func TestForkWith_Count3_JSON_Schema(t *testing.T) {
	svc := newTestService(t)
	ref := startedSandbox(t, svc, "proj/fork-json")

	out, stdout, _ := capture(true)
	if err := runForkWith(context.Background(), []string{ref, "--count", "3"}, out, svc); err != nil {
		t.Fatalf("runForkWith: %v", err)
	}

	var env struct {
		SchemaVersion int                 `json:"schema_version"`
		Kind          string              `json:"kind"`
		Data          sandboxListDataJSON `json:"data"`
	}
	decodeOne(t, stdout, &env)
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d, want 1", env.SchemaVersion)
	}
	if env.Kind != "fork.created" {
		t.Errorf("kind: got %q, want %q", env.Kind, "fork.created")
	}
	if len(env.Data.Sandboxes) != 3 {
		t.Errorf("sandboxes count: got %d, want 3", len(env.Data.Sandboxes))
	}
	for _, sb := range env.Data.Sandboxes {
		if sb.ID == "" {
			t.Error("child sandbox.id is empty")
		}
		if sb.State != "running" {
			t.Errorf("child state: got %q, want %q", sb.State, "running")
		}
	}
}

func TestForkWith_Count1_Default(t *testing.T) {
	svc := newTestService(t)
	ref := startedSandbox(t, svc, "proj/fork-default")

	out, stdout, _ := capture(true)
	if err := runForkWith(context.Background(), []string{ref}, out, svc); err != nil {
		t.Fatalf("runForkWith (default count): %v", err)
	}

	var env struct {
		Data sandboxListDataJSON `json:"data"`
	}
	decodeOne(t, stdout, &env)
	if len(env.Data.Sandboxes) != 1 {
		t.Errorf("default count: got %d children, want 1", len(env.Data.Sandboxes))
	}
}

func TestForkWith_UsageError_MissingRef(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)
	err := runForkWith(context.Background(), []string{}, out, svc)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestForkWith_UsageError_BadCount(t *testing.T) {
	svc := newTestService(t)
	out, _, _ := capture(false)
	err := runForkWith(context.Background(), []string{"proj/x", "--count", "0"}, out, svc)
	if err == nil {
		t.Fatal("expected UsageError, got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestForkWith_CountEquals_Flag(t *testing.T) {
	svc := newTestService(t)
	ref := startedSandbox(t, svc, "proj/fork-eq")

	out, stdout, _ := capture(true)
	if err := runForkWith(context.Background(), []string{ref, "--count=2"}, out, svc); err != nil {
		t.Fatalf("runForkWith --count=2: %v", err)
	}

	var env struct {
		Data sandboxListDataJSON `json:"data"`
	}
	// Use json.Decoder directly to avoid the decodeOne helper's "exactly one" assertion
	// potentially tripping on extra whitespace from EmitSuccess.
	if err := json.NewDecoder(stdout).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Sandboxes) != 2 {
		t.Errorf("--count=2: got %d children, want 2", len(env.Data.Sandboxes))
	}
}

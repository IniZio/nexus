package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
)

// forkPreflightHarness stands up a running parent with a real disk on disk, so
// ProjectForkBytes has something to measure.
func forkPreflightHarness(t *testing.T) (*Service, domain.Sandbox, string) {
	t.Helper()
	ctx := context.Background()
	diskDir := t.TempDir()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := New(st, fake.New(), lifecycle.New()).WithDiskDir(diskDir)

	parent := domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "fork-preflight-parent",
		Project: "proj",
		State:   domain.Created,
	}
	if err := st.Create(ctx, parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	started, err := svc.Start(ctx, parent.ID.String())
	if err != nil {
		t.Fatalf("Start(parent): %v", err)
	}
	// Give the parent a measurable footprint.
	if err := os.WriteFile(filepath.Join(diskDir, parent.ID.String()+".raw"),
		make([]byte, 128*1024), 0o600); err != nil {
		t.Fatalf("seed parent disk: %v", err)
	}
	return svc, started, diskDir
}

// TestFork_PreflightRefusalLeavesNoChildDisks is the fork half of TBD-PD-26.
// Fork is the largest allocator in nexus3 — it copies the parent's ENTIRE
// footprint once per child — and it was completely unguarded.
//
// The assertion that matters is the second one: a refusal must happen before
// the child create-intent leases are written, otherwise a refused fork still
// litters diskDir with intent files that the reaper must later clean up.
func TestFork_PreflightRefusalLeavesNoChildDisks(t *testing.T) {
	svc, parent, diskDir := forkPreflightHarness(t)

	before, err := os.ReadDir(diskDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var seen int64
	refuse := func(_ string, projected int64, _ string) (*DiskPreflightResult, error) {
		seen = projected
		return nil, errors.New("service: insufficient disk space: stub refusal")
	}

	_, err = svc.Fork(context.Background(), parent.ID.String(), 3, ForkDiskPreflight(refuse))
	if err == nil {
		t.Fatal("Fork succeeded despite a refusing disk preflight")
	}
	if !strings.Contains(err.Error(), "insufficient disk space") {
		t.Errorf("error does not name the cause: %v", err)
	}

	want := 3 * diskAllocatedBytes(filepath.Join(diskDir, parent.ID.String()+".raw"))
	if seen != want {
		t.Errorf("projected %d bytes for a 3-way fork, want %d (3 × parent footprint)", seen, want)
	}

	after, err := os.ReadDir(diskDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(after) != len(before) {
		var names []string
		for _, e := range after {
			names = append(names, e.Name())
		}
		t.Errorf("fork refused but diskDir gained %d entries: %v", len(after)-len(before), names)
	}
}

// TestFork_ForceSkipsPreflight verifies the --force escape hatch reaches Fork.
func TestFork_ForceSkipsPreflight(t *testing.T) {
	svc, parent, _ := forkPreflightHarness(t)

	called := false
	refuse := func(_ string, _ int64, _ string) (*DiskPreflightResult, error) {
		called = true
		return nil, errors.New("service: insufficient disk space: stub refusal")
	}

	if _, err := svc.Fork(context.Background(), parent.ID.String(), 1,
		ForkDiskPreflight(refuse), ForkForceDiskSpace()); err != nil {
		t.Fatalf("Fork with --force: %v", err)
	}
	if called {
		t.Error("preflight was consulted under --force")
	}
}

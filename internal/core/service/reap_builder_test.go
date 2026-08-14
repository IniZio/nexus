package service_test

// Tests for the stale __builder record reaping in service.List (R-REAP).
//
// These tests exercise the two load-bearing cases:
//  1. Stale orphan: a __builder record whose creator PID is dead gets reaped.
//  2. Live builder: a __builder record whose creator is the test process
//     (os.Getpid()) is NOT reaped — the most important correctness property.

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// deadPID returns the PID of a process that has already exited.
// It spawns "true" (exits immediately), waits for it, and returns its PID.
// After Wait returns, the kernel has reclaimed the PID table entry — any
// subsequent kill(pid, 0) returns ESRCH.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("deadPID: spawn true: %v", err)
	}
	return cmd.Process.Pid
}

// newBuilderRecord writes a __builder sandbox record directly into st with the
// given creatorPID and returns its ID.
func newBuilderRecord(t *testing.T, st store.Store, creatorPID int) domain.SandboxID {
	t.Helper()
	id := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:           id,
		Name:         id.String(),
		Project:      "__builder",
		State:        domain.Created,
		RemoveOnExit: true,
		CreatorPID:   creatorPID,
	}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatalf("newBuilderRecord: st.Create: %v", err)
	}
	return id
}

// TestList_ReapsStaleBuilderOrphan is the primary correctness test for R-REAP.
// It creates a __builder record whose CreatorPID is a process that has already
// exited, calls List, and asserts the record is deleted from the store.
func TestList_ReapsStaleBuilderOrphan(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())

	pid := deadPID(t)
	id := newBuilderRecord(t, st, pid)

	// Pre-condition: the record exists before List is called.
	if _, getErr := st.Get(context.Background(), id); getErr != nil {
		t.Fatalf("pre-condition: record should exist before List: %v", getErr)
	}

	// List triggers reapBuilders.
	if _, listErr := svc.List(context.Background()); listErr != nil {
		t.Fatalf("List: %v", listErr)
	}

	// Post-condition: the stale record must be gone.
	_, getErr := st.Get(context.Background(), id)
	if getErr == nil {
		t.Errorf("stale __builder record still present after List — reaping did not fire")
	}
}

// TestList_DoesNotReapLiveBuilder verifies the most important safety property:
// a __builder record whose CreatorPID is the current (live) process is NOT
// deleted. Deleting the record of a running build would be catastrophic.
func TestList_DoesNotReapLiveBuilder(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())

	// CreatorPID = this test process — provably alive.
	id := newBuilderRecord(t, st, os.Getpid())

	// List triggers reapBuilders.
	if _, listErr := svc.List(context.Background()); listErr != nil {
		t.Fatalf("List: %v", listErr)
	}

	// Post-condition: the record of the live builder must still be present.
	if _, getErr := st.Get(context.Background(), id); getErr != nil {
		t.Errorf("live __builder record was wrongly deleted: %v", getErr)
	}
}

// TestList_DoesNotReapBuilderWithZeroPID verifies that a __builder record with
// CreatorPID == 0 (written before R-REAP, or by a test stub) is left alone.
// Such records cannot be checked for liveness and are handled only by the
// existing __builder filter in List.
func TestList_DoesNotReapBuilderWithZeroPID(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, fake.New(), lifecycle.New())

	id := newBuilderRecord(t, st, 0) // zero PID — legacy / untraceable

	if _, listErr := svc.List(context.Background()); listErr != nil {
		t.Fatalf("List: %v", listErr)
	}

	// The record must NOT have been deleted (reaping is skipped for zero PID).
	if _, getErr := st.Get(context.Background(), id); getErr != nil {
		t.Errorf("__builder record with CreatorPID==0 was wrongly deleted: %v", getErr)
	}
}

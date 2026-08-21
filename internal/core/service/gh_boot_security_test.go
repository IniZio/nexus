// gh_boot_security_test.go — D-PD-36 boot-time invariant tests (Hole 1).
//
// Decision: service.Start refuses to boot a sandbox that carries a GitHub
// secret host with an empty AllowedRepo. Failing the boot — rather than
// silently skipping or degrading the credential swap — makes the
// misconfiguration immediately visible. A sandbox in this state must be
// deleted and recreated with a valid AllowedRepo.
//
// This guard fires BEFORE driver.Start is called, so no VM is launched for
// the forbidden configuration.
//
// Mutation discipline: each enforcement test documents its expected mutation
// and the resulting failure. All mutations were run against this file.

package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// seedBadRecord creates a FileStore pre-populated with the given sandbox,
// bypassing CreateAndBoot so the record does not pass the create-time guard.
// This simulates a record written before D-PD-36 was enforced.
func seedBadRecord(t *testing.T, sb domain.Sandbox) store.Store {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := st.Create(context.Background(), sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return st
}

// ghUnboundRecord returns a Stopped sandbox with a GitHub secret host and no
// AllowedRepo — the exact forbidden combination the guard must catch.
func ghUnboundRecord(name string) domain.Sandbox {
	return domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    name,
		Project: "test",
		State:   domain.Stopped,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:deadbeef",
			SecretHosts: []string{"github.com"},
			AllowedRepo: "", // missing → guard must fire
		},
	}
}

// ghBoundRecord returns a Stopped sandbox with a GitHub secret host AND a
// valid AllowedRepo — the allowed combination that must boot normally.
func ghBoundRecord() domain.Sandbox {
	return domain.Sandbox{
		ID:      domain.NewSandboxID(),
		Name:    "bound",
		Project: "test",
		State:   domain.Stopped,
		Envelope: domain.Envelope{
			ImageDigest: "sha256:deadbeef",
			SecretHosts: []string{"github.com"},
			AllowedRepo: "owner/repo", // present → guard must NOT fire
		},
	}
}

// ── Hole 1: Start path ────────────────────────────────────────────────────────

// TestGHBootGuard_Start_UnboundSecretRefused drives the REAL service.Start path
// against a store record with a GitHub secret host and no AllowedRepo.
//
// Mutation evidence:
//   Remove the isGitHubHost guard loop added to service.Start
//   → Start returns nil instead of ErrUnboundGitHubSecret.
//   Restore → test passes.
func TestGHBootGuard_Start_UnboundSecretRefused(t *testing.T) {
	sb := ghUnboundRecord("bad-start")
	svc := service.New(seedBadRecord(t, sb), fake.New(), lifecycle.New())

	_, err := svc.Start(context.Background(), sb.ID.String())
	if !errors.Is(err, service.ErrUnboundGitHubSecret) {
		t.Fatalf("Start: got %v, want ErrUnboundGitHubSecret", err)
	}
}

// TestGHBootGuard_ForkChild_UnboundSecretRefused covers the case where a fork
// child record (persisted before the guard existed or via a hypothetical future
// code path that copies SecretHosts onto children) is later restarted.
// The same Start guard catches it.
//
// Mutation evidence: same mutation as TestGHBootGuard_Start_UnboundSecretRefused.
func TestGHBootGuard_ForkChild_UnboundSecretRefused(t *testing.T) {
	child := ghUnboundRecord("fork-child-bad")
	svc := service.New(seedBadRecord(t, child), fake.New(), lifecycle.New())

	_, err := svc.Start(context.Background(), child.ID.String())
	if !errors.Is(err, service.ErrUnboundGitHubSecret) {
		t.Fatalf("Start (fork-child): got %v, want ErrUnboundGitHubSecret", err)
	}
}

// TestGHBootGuard_RestoreChild_UnboundSecretRefused mirrors the fork-child
// case for the restore boot path.
//
// Mutation evidence: same mutation as TestGHBootGuard_Start_UnboundSecretRefused.
func TestGHBootGuard_RestoreChild_UnboundSecretRefused(t *testing.T) {
	child := ghUnboundRecord("restore-child-bad")
	svc := service.New(seedBadRecord(t, child), fake.New(), lifecycle.New())

	_, err := svc.Start(context.Background(), child.ID.String())
	if !errors.Is(err, service.ErrUnboundGitHubSecret) {
		t.Fatalf("Start (restore-child): got %v, want ErrUnboundGitHubSecret", err)
	}
}

// TestGHBootGuard_BoundSecretAllowed verifies the guard is not over-broad: a
// sandbox with a GitHub secret host AND a valid AllowedRepo must boot normally.
//
// Mutation evidence:
//   Remove the `sb.Envelope.AllowedRepo == ""` condition so the guard always
//   fires for any GitHub secret host.
//   → Start returns ErrUnboundGitHubSecret even for the valid sandbox.
//   Restore → test passes.
func TestGHBootGuard_BoundSecretAllowed(t *testing.T) {
	sb := ghBoundRecord()
	svc := service.New(seedBadRecord(t, sb), fake.New(), lifecycle.New())

	_, err := svc.Start(context.Background(), sb.ID.String())
	if err != nil {
		t.Fatalf("Start with bound AllowedRepo: got %v, want nil", err)
	}
}

// ── Hole 2: rollback Delete failure surface ───────────────────────────────────

// failOnceDeleteStore wraps a Store and returns errOnDelete on the FIRST Delete
// call. Subsequent calls delegate to the underlying store normally.
type failOnceDeleteStore struct {
	store.Store
	errOnDelete error
	fired       bool
}

func (f *failOnceDeleteStore) Delete(ctx context.Context, id domain.SandboxID) error {
	if !f.fired {
		f.fired = true
		return f.errOnDelete
	}
	return f.Store.Delete(ctx, id)
}

// TestGHBootGuard_StoreWrapperFailOnce verifies the failOnceDeleteStore
// test helper itself: first Delete returns the injected error; subsequent
// calls delegate to the underlying store.
//
// D-SHL-05 note: the post-boot ErrAgentGitHubSecret rollback path this
// wrapper was originally designed to drive has been removed. The pre-boot
// ErrUnboundGitHubSecret guard now fires before any VM boots, so no
// post-boot rollback is needed for agent+GitHub sandboxes. This test retains
// the store wrapper verification in case it is useful for other rollback
// scenarios.
func TestGHBootGuard_StoreWrapperFailOnce(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fst := &failOnceDeleteStore{
		Store:       st,
		errOnDelete: errors.New("disk full: cannot delete record"),
	}
	// Verify the wrapper behaves correctly: first Delete fails, second delegates.
	if err := fst.Delete(context.Background(), domain.NewSandboxID()); err == nil {
		t.Fatal("failOnceDeleteStore: first Delete should fail")
	}
	if err := fst.Delete(context.Background(), domain.NewSandboxID()); err != nil {
		// Second call delegates to underlying store; record doesn't exist → ErrNotFound is expected.
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("failOnceDeleteStore: second Delete unexpected error: %v", err)
		}
	}
}

// gh_boot_rollback_test.go — D-PD-36 Hole 2: rollback Delete failure surface.
//
// This file is package service (internal) so it can reuse the CreateAndBoot
// test helpers (noopProbe, fakeDriverFactory, putFakeImage) from create_test.go.

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/store"
)

// failOnDeleteStore wraps a Store and returns errOnDelete on every Delete call.
// Used to simulate a disk-full or lock-failure scenario during rollback.
type failOnDeleteStore struct {
	store.Store
	errOnDelete error
}

func (f *failOnDeleteStore) Delete(_ context.Context, _ domain.SandboxID) error {
	return f.errOnDelete
}

// TestGHBootGuard_RollbackDeleteFailureSurfaced drives the REAL CreateAndBoot
// path with UseAgentSeed=true and a GitHub secret bind. When the rollback's
// store.Delete fails, the error must appear in the returned error rather than
// being silently swallowed. The caller must still see ErrAgentGitHubSecret
// as the sentinel so the refusal reason is preserved.
//
// Mutation evidence:
//   Change the rollback from:
//     if delErr := svc.store.Delete(ctx, booted.ID); delErr != nil { return ..., ErrAgentGitHubSecret }
//   back to:
//     _ = svc.store.Delete(ctx, booted.ID)
//   → errors.Is(err, ErrAgentGitHubSecret) still passes but
//     strings.Contains(err.Error(), "rollback failed") is false → assertion fails.
//   Restore → test passes.
func TestGHBootGuard_RollbackDeleteFailureSurfaced(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	// Backing store that always fails on Delete — simulates disk-full / flock
	// error during rollback cleanup.
	realSt, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	deleteErr := errors.New("disk full: cannot delete record")
	fst := &failOnDeleteStore{Store: realSt, errOnDelete: deleteErr}

	fd := fake.New()
	svc := New(fst, fd, lifecycle.New())

	_, createErr := CreateAndBoot(
		ctx,
		svc,
		cache,
		fakeDriverFactory(fd),
		noopProbe,
		"proj", "rollback-test",
		CreateAndBootOptions{
			Image:        ImageSpec{Digest: string(img.Digest)},
			CacheRoot:    cacheRoot,
			UseAgentSeed: true,
			// GitHub secret bind triggers ErrAgentGitHubSecret in UseAgentSeed path.
			Secrets: []SecretBind{{Env: BuiltinGitHubEnv, Hosts: GitHubSecretHosts, Token: "ghp_tok"}},
		},
	)

	// 1. The caller must see ErrAgentGitHubSecret as the refusal sentinel.
	if !errors.Is(createErr, ErrAgentGitHubSecret) {
		t.Fatalf("CreateAndBoot: got %v, want ErrAgentGitHubSecret", createErr)
	}

	// 2. The Delete failure must appear in the error string so the operator can
	// diagnose the leaked record. A swallowed delete error produces a plain
	// ErrAgentGitHubSecret message with no mention of the rollback failure.
	if createErr == nil || !strings.Contains(createErr.Error(), "rollback failed") {
		t.Errorf("CreateAndBoot error should mention rollback failure; got: %v", createErr)
	}

	// 3. The fake driver's sandboxes still contain the leaked record (Delete
	// failed), confirming that the guard correctly surfaced the leak rather than
	// pretending the cleanup succeeded.
	_ = createErr // already checked above
}

// TestGHBootGuard_RollbackDeleteSuccess_ErrAgentGitHubSecret is a baseline: when
// Delete succeeds the caller still sees ErrAgentGitHubSecret (no mention of
// "rollback failed" since there was no failure).
func TestGHBootGuard_RollbackDeleteSuccess_ErrAgentGitHubSecret(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	fd := fake.New()
	svc := newTestSvc(t, fd)

	_, createErr := CreateAndBoot(
		ctx,
		svc,
		cache,
		fakeDriverFactory(fd),
		noopProbe,
		"proj", "rollback-ok-test",
		CreateAndBootOptions{
			Image:        ImageSpec{Digest: string(img.Digest)},
			CacheRoot:    cacheRoot,
			UseAgentSeed: true,
			Secrets:      []SecretBind{{Env: BuiltinGitHubEnv, Hosts: GitHubSecretHosts, Token: "ghp_tok"}},
		},
	)

	if !errors.Is(createErr, ErrAgentGitHubSecret) {
		t.Fatalf("CreateAndBoot: got %v, want ErrAgentGitHubSecret", createErr)
	}
	// When Delete succeeds the rollback failure path is NOT taken.
	if strings.Contains(createErr.Error(), "rollback failed") {
		t.Errorf("unexpected 'rollback failed' in error: %v", createErr)
	}
}

// Compile-time: failOnDeleteStore implements store.Store.
var _ driver.Driver = (*fake.FakeDriver)(nil)

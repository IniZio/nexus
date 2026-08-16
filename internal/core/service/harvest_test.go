package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

// fakeContent is written to the destination file by the fake copier.
const fakeContent = "harvest-artifact"

// makeFakeCopy returns a harvestCopyFn that writes fakeContent to destFile.
// If failFor contains a SandboxID the copier returns an error for that sandbox
// instead.
func makeFakeCopy(failFor ...domain.SandboxID) harvestCopyFn {
	failSet := make(map[domain.SandboxID]bool, len(failFor))
	for _, id := range failFor {
		failSet[id] = true
	}
	return func(_ context.Context, sb domain.Sandbox, _, destFile string) error {
		if failSet[sb.ID] {
			return errors.New("injected copy failure")
		}
		return os.WriteFile(destFile, []byte(fakeContent+"-"+sb.ID.String()), 0o644)
	}
}

// seedSandbox stores a domain.Sandbox directly in svc's store and returns it.
// The fake driver is used so no actual VM is needed.
func seedSandbox(t *testing.T, ctx context.Context, svc *Service, motiveID string) domain.Sandbox {
	t.Helper()
	sb := domain.Sandbox{
		ID:     domain.NewSandboxID(),
		Name:   "harvest-test-" + motiveID,
		Labels: map[string]string{"motive": motiveID},
		State:  domain.Created,
	}
	if err := svc.store.Create(ctx, sb); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return sb
}

// TestHarvestMotive_AllSucceed verifies that harvestMotive copies from every
// sandbox of a motive into the right per-sandbox host paths.
func TestHarvestMotive_AllSucceed(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const motive = "motive-all-ok"
	sb1 := seedSandbox(t, ctx, svc, motive)
	sb2 := seedSandbox(t, ctx, svc, motive)

	hostDir := t.TempDir()
	result, err := svc.harvestMotive(ctx, motive, "/guest/result.txt", hostDir, makeFakeCopy())
	if err != nil {
		t.Fatalf("harvestMotive returned unexpected error: %v", err)
	}

	if len(result.Outcomes) != 2 {
		t.Fatalf("expected 2 outcomes, got %d", len(result.Outcomes))
	}

	for _, o := range result.Outcomes {
		if o.Err != nil {
			t.Errorf("sandbox %s: unexpected error: %v", o.SandboxID, o.Err)
		}

		// Host path must be under hostDir/<sandboxID>/result.txt
		wantPath := filepath.Join(hostDir, o.SandboxID.String(), "result.txt")
		if o.HostPath != wantPath {
			t.Errorf("sandbox %s: HostPath = %q, want %q", o.SandboxID, o.HostPath, wantPath)
		}

		// File must exist and have the fake content.
		data, err := os.ReadFile(o.HostPath)
		if err != nil {
			t.Errorf("sandbox %s: ReadFile(%q): %v", o.SandboxID, o.HostPath, err)
			continue
		}
		want := fakeContent + "-" + o.SandboxID.String()
		if string(data) != want {
			t.Errorf("sandbox %s: file content = %q, want %q", o.SandboxID, data, want)
		}
	}

	// All sandbox IDs must be present.
	ids := map[domain.SandboxID]bool{}
	for _, o := range result.Outcomes {
		ids[o.SandboxID] = true
	}
	if !ids[sb1.ID] || !ids[sb2.ID] {
		t.Errorf("outcomes missing one or both sandbox IDs; got %v, want %v and %v", ids, sb1.ID, sb2.ID)
	}
}

// TestHarvestMotive_PartialFailure verifies that when one sandbox's pull fails,
// the remaining sandboxes are still harvested and the aggregate error identifies
// exactly which sandbox failed.
func TestHarvestMotive_PartialFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	const motive = "motive-partial"
	sbGood := seedSandbox(t, ctx, svc, motive)
	sbBad := seedSandbox(t, ctx, svc, motive)

	hostDir := t.TempDir()
	result, err := svc.harvestMotive(ctx, motive, "/guest/out", hostDir, makeFakeCopy(sbBad.ID))

	// Must return a non-nil error because sbBad failed.
	if err == nil {
		t.Fatal("expected non-nil error for partial failure, got nil")
	}

	if len(result.Outcomes) != 2 {
		t.Fatalf("expected 2 outcomes, got %d", len(result.Outcomes))
	}

	outcomeFor := map[domain.SandboxID]SandboxHarvestOutcome{}
	for _, o := range result.Outcomes {
		outcomeFor[o.SandboxID] = o
	}

	// sbGood must have succeeded.
	good := outcomeFor[sbGood.ID]
	if good.Err != nil {
		t.Errorf("sbGood: expected no error, got %v", good.Err)
	}
	if good.HostPath == "" {
		t.Errorf("sbGood: HostPath must be non-empty on success")
	}
	if _, statErr := os.Stat(good.HostPath); statErr != nil {
		t.Errorf("sbGood: artifact file missing: %v", statErr)
	}

	// sbBad must have a recorded failure and no HostPath.
	bad := outcomeFor[sbBad.ID]
	if bad.Err == nil {
		t.Errorf("sbBad: expected an error, got nil")
	}
	if bad.HostPath != "" {
		t.Errorf("sbBad: HostPath must be empty on failure, got %q", bad.HostPath)
	}
}

// TestHarvestMotive_EmptyMotive verifies that a motive with no sandboxes
// returns an empty result without crashing and without error.
func TestHarvestMotive_EmptyMotive(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t, fake.New())

	hostDir := t.TempDir()
	result, err := svc.harvestMotive(ctx, "motive-empty", "/guest/x", hostDir, makeFakeCopy())
	if err != nil {
		t.Fatalf("empty motive: unexpected error: %v", err)
	}
	if len(result.Outcomes) != 0 {
		t.Errorf("expected 0 outcomes for empty motive, got %d", len(result.Outcomes))
	}
}
